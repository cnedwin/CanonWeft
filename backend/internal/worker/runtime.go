package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	temporalworker "go.temporal.io/sdk/worker"
	"google.golang.org/protobuf/types/known/durationpb"
	"novelstudio/internal/models"
	"novelstudio/internal/research"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"sync"
	"time"
)

const ReviewQueue = "platform-review-v1"

type Runtime struct {
	client       client.Client
	worker       temporalworker.Worker
	cancel       context.CancelFunc
	done         sync.WaitGroup
	db           *sql.DB
	cleanup      *service.RetentionScanner
	closeCleanup func() error
}

// RuntimeServices allows trusted process composition to supply the model gateway.
// HTTP payloads and environment variables cannot inject execution implementations.
type RuntimeServices struct {
	ResearchProvider service.ResearchProvider
	Generations      *service.Generations
	Retention        *service.RetentionScanner
}

func StartRuntime(ctx context.Context, tasks *service.Tasks, address, namespace string, dependencies ...RuntimeServices) (*Runtime, error) {
	if address == "" || namespace == "" {
		return nil, errors.New("持久任务服务地址或命名空间尚未配置")
	}
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Namespace: namespace})
	if err != nil {
		return nil, errors.New("无法连接持久任务服务")
	}
	_, err = c.WorkflowService().RegisterNamespace(ctx, &workflowservice.RegisterNamespaceRequest{Namespace: namespace, WorkflowExecutionRetentionPeriod: durationpb.New(7 * 24 * time.Hour)})
	var exists *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &exists) {
		c.Close()
		return nil, errors.New("持久任务命名空间不可用")
	}
	w := temporalworker.New(c, ReviewQueue, temporalworker.Options{})
	registerTemporaryCleanup(w)
	var cleanup *service.RetentionScanner
	var closeCleanup func() error
	if os.Getenv("STUDIO_RETENTION_EXECUTION_ENABLED") == "1" {
		if len(dependencies) > 0 {
			cleanup = dependencies[0].Retention
		}
		if cleanup == nil {
			// Bind the same configured application store, never an unrelated
			// environment path supplied separately to the cleaner.
			local, ok := tasks.IP.Blobs.(storage.LocalBlobs)
			if !ok {
				c.Close()
				return nil, errors.New("清理存储尚未绑定")
			}
			bound, err := storage.NewLocalRetentionStore(local.Root)
			if err != nil {
				c.Close()
				return nil, errors.New("清理存储不可用")
			}
			closeCleanup = bound.Close
			cleanup = &service.RetentionScanner{Workspace: tasks.IP.Workspace, Blobs: bound}
		}
		RegisterCleanup(w, cleanup)
	}
	RegisterReview(w, tasks)
	RegisterChangePropagation(w, tasks.IP)
	RegisterIngest(w, &service.IngestJobs{IP: tasks.IP})
	RegisterDiscovery(w, &service.SkillDiscovery{IP: tasks.IP})
	generations := &service.Generations{IP: tasks.IP, Adapter: models.New(nil)}
	if len(dependencies) > 0 && dependencies[0].Generations != nil {
		generations = dependencies[0].Generations
	}
	RegisterGeneration(w, generations)
	RegisterRecipe(w, &service.RecipeExecutions{Generations: generations})
	var researchProvider service.ResearchProvider = research.NewCatalog(nil)
	if len(dependencies) > 0 && dependencies[0].ResearchProvider != nil {
		researchProvider = dependencies[0].ResearchProvider
	}
	RegisterResearchExecutions(w, &service.NovelResearchExecutions{Research: &service.NovelResearch{Generations: generations}, Sources: &service.ResearchSources{IP: tasks.IP, Provider: researchProvider}})
	embedder, _ := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	RegisterSemanticReview(w, &service.SemanticReviews{Generations: generations, Knowledge: &service.Knowledge{IP: tasks.IP, Embedder: embedder}})
	RegisterIndex(w, &service.IndexJobs{Knowledge: &service.Knowledge{IP: tasks.IP, Embedder: embedder}})
	RegisterToolTrials(w, &service.ToolTrials{IP: tasks.IP, RuntimeRoot: os.Getenv("STUDIO_ISOLATION_ROOT")})
	RegisterQualification(w, &service.Qualifications{IP: tasks.IP, RuntimeRoot: os.Getenv("STUDIO_ISOLATION_ROOT")})
	if err = w.Start(); err != nil {
		if closeCleanup != nil {
			_ = closeCleanup()
		}
		c.Close()
		return nil, errors.New("持久任务工作进程启动失败")
	}
	running, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{client: c, worker: w, cancel: cancel, db: tasks.IP.Workspace.DB, cleanup: cleanup, closeCleanup: closeCleanup}
	runtime.done.Add(1)
	go func() { defer runtime.done.Done(); runtime.deliver(running) }()
	return runtime, nil
}
func (r *Runtime) Close() {
	r.cancel()
	r.done.Wait()
	r.worker.Stop()
	r.client.Close()
	if r.closeCleanup != nil {
		_ = r.closeCleanup()
	}
}
func (r *Runtime) Wake(ctx context.Context, id string) error {
	return r.client.SignalWorkflow(ctx, "review-"+id, "", "wake", "")
}
func (r *Runtime) WakeGeneration(ctx context.Context, id string) error {
	return r.client.SignalWorkflow(ctx, "generation-"+id, "", "wake", "")
}

// This loop only delivers transactional outbox messages. Business execution,
// waiting, timers and retries are owned by Temporal, not by this process loop.
func (r *Runtime) deliver(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		delivery, cancel := context.WithTimeout(ctx, 5*time.Second)
		r.deliverBatch(delivery)
		cancel()
		cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 5*time.Second)
		r.deliverCleanupBatch(cleanupCtx)
		r.deliverTemporaryCleanup(cleanupCtx)
		cleanupCancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (r *Runtime) deliverBatch(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, "SELECT id,resource_id,kind,payload FROM outbox WHERE kind IN ('task.start','generation.start','index.start','discovery.start','discovery.schedule','tool.start','qualification.start','qualification.schedule','qualification.receipt','semantic.review.start','recipe.start','recipe.retry','ip.published','release.published','ingest.start','research.step') AND processed_at IS NULL ORDER BY created_at,id LIMIT 20")
	if err != nil {
		return
	}
	type item struct {
		id, task, kind string
		payload        []byte
	}
	pending := []item{}
	for rows.Next() {
		var v item
		if err = rows.Scan(&v.id, &v.task, &v.kind, &v.payload); err != nil {
			rows.Close()
			return
		}
		pending = append(pending, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, v := range pending {
		if _, err := retention.ParseSourceProjection(v.payload, "outbox.payload"); err == nil {
			continue
		}
		workflowKey := v.task
		var workflowFn interface{} = ReviewWorkflow
		prefix := "review-"
		if v.kind == "research.step" {
			workflowFn = ResearchExecutionWorkflow
			prefix = "research-checkpoint-"
			workflowKey = v.id
			v.task = v.id
		}
		if v.kind == "ip.published" || v.kind == "release.published" {
			workflowFn = ChangePropagationWorkflow
			prefix = "publication-propagation-"
			workflowKey = v.id
			v.task = v.id
		}
		if v.kind == "generation.start" {
			workflowFn = GenerationWorkflow
			prefix = "generation-"
		}
		if v.kind == "ingest.start" {
			workflowFn = IngestWorkflow
			prefix = "ingest-"
		}
		if v.kind == "semantic.review.start" {
			workflowFn = SemanticReviewWorkflow
			prefix = "semantic-review-"
		}
		if v.kind == "recipe.start" || v.kind == "recipe.retry" {
			workflowFn = RecipeWorkflow
			prefix = "recipe-"
		}
		if v.kind == "index.start" {
			workflowFn = IndexWorkflow
			prefix = "index-"
		}
		if v.kind == "discovery.start" {
			workflowFn = DiscoveryWorkflow
			prefix = "discovery-"
		}
		if v.kind == "discovery.schedule" {
			workflowFn = DiscoveryScheduleWorkflow
			prefix = "discovery-schedule-"
		}
		if v.kind == "tool.start" {
			workflowFn = ToolTrialWorkflow
			prefix = "tool-"
		}
		if v.kind == "qualification.start" {
			workflowFn = QualificationWorkflow
			prefix = "qualification-"
		}
		if v.kind == "qualification.schedule" {
			workflowFn = QualificationScheduleWorkflow
			prefix = "qualification-schedule-"
			var payload struct {
				ScheduleID string `json:"schedule_id"`
			}
			if json.Unmarshal(v.payload, &payload) != nil {
				continue
			}
			if payload.ScheduleID != "" {
				v.task = payload.ScheduleID
			}
			workflowKey = v.task
		}
		if v.kind == "qualification.receipt" {
			var payload struct {
				RunID string `json:"run_id"`
			}
			if json.Unmarshal(v.payload, &payload) != nil || payload.RunID == "" {
				continue
			}
			v.task = payload.RunID
			workflowKey = v.id
			workflowFn = QualificationReceiptWorkflow
			prefix = "qualification-receipt-"
		}
		options := client.StartWorkflowOptions{ID: prefix + workflowKey, TaskQueue: ReviewQueue, WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}
		if v.kind == "recipe.start" || v.kind == "recipe.retry" {
			// Manual rework can resume a completed execution. Replayed events
			// only wake it; SQL attempts and child IDs own call idempotency.
			options.WorkflowIDReusePolicy = enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE
			_, err = r.client.SignalWithStartWorkflow(ctx, options.ID, "wake", "", options, workflowFn, v.task)
		} else if v.kind == "qualification.schedule" {
			_, err = r.client.SignalWithStartWorkflow(ctx, options.ID, "wake", "", options, workflowFn, v.task)
		} else {
			_, err = r.client.ExecuteWorkflow(ctx, options, workflowFn, v.task)
		}
		var already *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &already) {
			continue
		}
		// If this acknowledgement is lost, the same workflow ID is safely redelivered.
		_, _ = r.db.ExecContext(ctx, "UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=? AND processed_at IS NULL", v.id)
	}
}
