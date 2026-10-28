package isolation

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"novelstudio/internal/executiontemp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// File is an approved byte copy, never a host filesystem reference.
type File struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type Request struct {
	Language       string
	Script         string
	Files          []File
	Entrypoint     string
	Input          []byte
	Timeout        time.Duration
	MaxOutputBytes int
}

type Result struct {
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Reason    string `json:"reason"`
	ImageHash string `json:"image_hash"`
	InputHash string `json:"input_hash"`
}

// Runner accepts only approved bytes. It never accepts host paths, environment,
// arbitrary QEMU options, network capabilities, or publication authority.
type Runner struct {
	root      string
	imageHash string
}

// All runners in the service share a single VM budget. Waiting is cancellable.
var runSlot = make(chan struct{}, 1)

func NewRunner(root, imageHash string) (*Runner, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	runner := &Runner{root: absolute, imageHash: imageHash}
	if err := runner.verify(); err != nil {
		return nil, err
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context, request Request) (out Result, runErr error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	select {
	case runSlot <- struct{}{}:
		defer func() { <-runSlot }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	// Bound package serialization as part of the shared VM budget too.
	payload, err := prepareRequest(request)
	if err != nil {
		return Result{}, err
	}
	if err := r.verify(); err != nil {
		return Result{}, err
	}
	inputDigest := sha256.Sum256(payload)
	temporary, err := executiontemp.Create(filepath.Join(filepath.Dir(r.root), "execution-temporary"))
	if err != nil {
		return Result{}, err
	}
	jobDirectory := temporary.Path()
	defer func() {
		if cleanupErr := temporary.Close(); cleanupErr != nil {
			out = Result{}
			runErr = errors.Join(runErr, fmt.Errorf("isolation: input copy cleanup failed: %w", cleanupErr))
		}
	}()
	initrd := filepath.Join(jobDirectory, "input-initramfs.gz")
	if err := writeInputImage(filepath.Join(r.root, "initramfs.gz"), initrd, payload); err != nil {
		return Result{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, request.Timeout+30*time.Second)
	defer cancel()
	qemu := filepath.Join(r.root, "qemu", "qemu-system-x86_64.exe")
	command := exec.Command(qemu,
		"-no-user-config", "-nodefaults", "-machine", "pc", "-accel", "tcg,thread=single,tb-size=16",
		"-cpu", "max", "-m", "256", "-smp", "1", "-nic", "none", "-display", "none",
		"-vga", "none", "-serial", "stdio", "-monitor", "none", "-no-reboot",
		"-L", filepath.Join(r.root, "qemu", "share"),
		"-kernel", filepath.Join(r.root, "vmlinuz-virt"),
		"-initrd", initrd,
		"-append", "console=ttyS0 quiet panic=-1 rdinit=/init")
	command.Dir = filepath.Join(r.root, "qemu")
	command.Env = cleanEnvironment(command.Dir)
	var diagnostic boundedBuffer
	command.Stderr = &diagnostic
	process, err := startContained(runContext, command)
	if err != nil {
		return Result{}, fmt.Errorf("isolation: contained emulator creation: %w", err)
	}
	defer process.Close()
	scanner := bufio.NewScanner(process.Stdout)
	scanner.Buffer(make([]byte, 4096), request.MaxOutputBytes*2+16384)
	var result Result
	var bootDiagnostic boundedBuffer
	ready, started, received := false, false, false
	var protocolErr error
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "STUDIO_READY" && !ready {
			ready = true
		} else if line == "STUDIO_STARTED" && ready {
			started = true
		} else if strings.HasPrefix(line, "STUDIO_RESULT:") && ready && !received {
			protocolErr = json.Unmarshal([]byte(strings.TrimPrefix(line, "STUDIO_RESULT:")), &result)
			received = protocolErr == nil
			break
		} else if strings.HasPrefix(line, "STUDIO_ERROR:") {
			protocolErr = fmt.Errorf("isolation: guest rejected execution: %s", line)
			break
		} else if !ready {
			_, _ = bootDiagnostic.Write([]byte(line + "\n"))
		}
	}
	if protocolErr == nil {
		protocolErr = scanner.Err()
	}
	// The result is data only. Destroy the entire guest even if scripts daemonize.
	_ = process.Kill()
	waitErr := process.Wait()
	if ctx.Err() != nil {
		if started {
			return Result{}, fmt.Errorf("isolation: canceled during script execution: %w", ctx.Err())
		}
		return Result{}, ctx.Err()
	}
	if runContext.Err() != nil {
		return Result{}, fmt.Errorf("isolation: VM deadline: %w; boot: %s; emulator: %s", runContext.Err(), bootDiagnostic.String(), diagnostic.String())
	}
	if protocolErr != nil {
		return Result{}, fmt.Errorf("isolation: protocol: %w", protocolErr)
	}
	if !received {
		return Result{}, fmt.Errorf("isolation: guest result missing (%v); boot: %s; emulator: %s", waitErr, bootDiagnostic.String(), diagnostic.String())
	}
	if len(result.Stdout)+len(result.Stderr) > request.MaxOutputBytes || (result.Reason != "completed" && result.Reason != "timeout" && result.Reason != "output_limit") {
		return Result{}, errors.New("isolation: invalid result envelope")
	}
	result.ImageHash = r.imageHash
	result.InputHash = hex.EncodeToString(inputDigest[:])
	return result, nil
}

func writeInputImage(base, destination string, payload []byte) error {
	source, err := os.Open(base)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer target.Close()
	if _, err = io.Copy(target, source); err != nil {
		return err
	}
	compressed := gzip.NewWriter(target)
	if err = writeCPIOEntry(compressed, "request.json", payload, 0100600, 1); err != nil {
		return err
	}
	if err = writeCPIOEntry(compressed, "TRAILER!!!", nil, 0, 2); err != nil {
		return err
	}
	if err = compressed.Close(); err != nil {
		return err
	}
	return target.Sync()
}

func writeCPIOEntry(writer io.Writer, name string, data []byte, mode, inode int) error {
	fields := []int{inode, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name) + 1, 0}
	var header strings.Builder
	header.WriteString("070701")
	for _, field := range fields {
		fmt.Fprintf(&header, "%08x", field)
	}
	header.WriteString(name)
	header.WriteByte(0)
	for header.Len()%4 != 0 {
		header.WriteByte(0)
	}
	if _, err := io.WriteString(writer, header.String()); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	_, err := writer.Write(make([]byte, (4-len(data)%4)%4))
	return err
}

func (r *Runner) verify() error {
	data, err := os.ReadFile(filepath.Join(r.root, "manifest.json"))
	if err != nil {
		return fmt.Errorf("isolation: runtime unavailable: %w", err)
	}
	digest := sha256.Sum256(data)
	if len(r.imageHash) != 64 || hex.EncodeToString(digest[:]) != r.imageHash {
		return errors.New("isolation: runtime manifest hash mismatch")
	}
	var manifest struct {
		Format int               `json:"format"`
		Files  map[string]string `json:"files"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Format != 1 || len(manifest.Files) < 4 {
		return errors.New("isolation: invalid runtime manifest")
	}
	for _, name := range []string{"vmlinuz-virt", "initramfs.gz", "qemu/qemu-system-x86_64.exe", "qemu/share/bios-256k.bin"} {
		if len(manifest.Files[name]) != 64 {
			return fmt.Errorf("isolation: required runtime artifact missing: %s", name)
		}
	}
	for name, expected := range manifest.Files {
		if !filepath.IsLocal(name) || strings.Contains(name, "\\") || strings.Contains(name, ":") {
			return errors.New("isolation: unsafe manifest path")
		}
		file, err := os.Open(filepath.Join(r.root, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("isolation: runtime artifact unavailable: %s", name)
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || hex.EncodeToString(hash.Sum(nil)) != expected {
			return fmt.Errorf("isolation: runtime artifact hash mismatch: %s", name)
		}
	}
	return filepath.WalkDir(r.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("isolation: runtime link rejected")
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(r.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative != "manifest.json" && relative != "image.sha256" && manifest.Files[relative] == "" {
			return fmt.Errorf("isolation: unpinned runtime artifact: %s", relative)
		}
		return nil
	})
}

type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 4096 - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedBuffer) String() string { return string(b.data) }
