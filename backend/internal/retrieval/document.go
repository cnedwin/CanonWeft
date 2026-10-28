package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"novelstudio/internal/executiontemp"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// DocumentError carries only a fixed public message, never parser diagnostics.
type DocumentError struct{ Code string }

func (e *DocumentError) Error() string { return documentReason(e.Code) }
func (e *DocumentError) Unwrap() error { return ErrImport }

func documentReason(code string) string {
	messages := map[string]string{
		"encrypted":                "PDF 已加密，请先在本地解密后重新导入",
		"corrupt":                  "文件损坏或格式与扩展名不符，无法可靠读取",
		"input_limit":              "文件为空或超过 32 MiB 上限",
		"page_limit":               "PDF 超过 100 页，请拆分后导入",
		"pixel_limit":              "图片或页面超过 2500 万像素或 12000 像素边长上限",
		"animated_image":           "动态图片包含多个画面，请分别导入静态图片",
		"runtime_unavailable":      "本地 PDF/图片解析环境不可用，请运行 install-ingest.ps1 安装并验证",
		"ocr_unavailable":          "本地 OCR 模型缺失或校验失败，此页未识别",
		"ocr_disabled":             "本地 OCR 已关闭，此页没有可提取的原生文字",
		"no_text_visual_reference": "未识别到文字；保留为视觉参考，未执行视觉内容理解",
		"low_confidence":           "OCR 置信度不足，请核对原图；候选文字未自动加入导入正文",
		"resource_limit":           "本页解析达到内存或 CPU 资源限制，未完成提取",
		"ocr_resource_or_failure":  "本页 OCR 失败或达到资源限制，未完成提取",
		"page_timeout":             "本页解析超过 30 秒，未完成提取",
		"file_timeout":             "文件解析达到 40 秒上限，此页未完成处理",
		"busy":                     "本地文件解析正在处理其他文件，请稍后重试",
		"text_limit":               "提取文字达到 256 KiB 上限，此单元未完整提取，请拆分文件",
		"page_unreadable":          "页面无法可靠读取，请核对原文件",
	}
	if message, ok := messages[code]; ok {
		return message
	}
	return "文件解析未完成，请核对原文件或本地解析环境"
}

// Only one local ingest runs at once; waiting honors request cancellation.
var documentSlots = make(chan struct{}, 1)

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 2*1024*1024 {
		return 0, fmt.Errorf("document output limit")
	}
	return b.Buffer.Write(data)
}

func documentPaths() (string, string, string, error) {
	// Deployment may explicitly provide both fixed trusted paths. Attachment names never select executables.
	python, script := os.Getenv("STUDIO_INGEST_PYTHON"), os.Getenv("STUDIO_INGEST_SCRIPT")
	if python != "" || script != "" {
		if !filepath.IsAbs(python) || !filepath.IsAbs(script) {
			return "", "", "", &DocumentError{"runtime_unavailable"}
		}
	} else {
		directory, err := os.Getwd()
		if err != nil {
			return "", "", "", err
		}
		for {
			candidate := filepath.Join(directory, "tools", "ingest", "worker.py")
			if _, err = os.Stat(candidate); err == nil {
				script = candidate
				python = filepath.Join(filepath.Dir(directory), "work", "ingest-venv", "Scripts", "python.exe")
				break
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
			directory = parent
		}
	}
	for _, name := range []string{python, script} {
		if info, err := os.Stat(name); err != nil || info.IsDir() {
			return "", "", "", &DocumentError{"runtime_unavailable"}
		}
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(script)))
	return python, script, filepath.Join(root, ".runtime", "work", "ingest"), nil
}

func extractDocument(ctx context.Context, name string, data []byte) (ImportReport, error) {
	mode := "enabled"
	if os.Getenv("STUDIO_INGEST_OCR") == "disabled" {
		mode = "disabled"
	}
	report, _, err := extractDocumentPart(ctx, name, data, "file", 0, mode)
	return report, err
}

func extractDocumentPart(ctx context.Context, name string, data []byte, phase string, page int, mode string) (ImportReport, int, error) {
	report := ImportReport{Units: []ImportUnit{}}
	if (phase != "file" && phase != "plan" && phase != "unit") || page < 0 || page >= 100 || (mode != "enabled" && mode != "disabled") {
		return report, 0, ErrImport
	}
	waitContext, stopWait := context.WithTimeout(ctx, 2*time.Second)
	defer stopWait()
	select {
	case documentSlots <- struct{}{}:
		defer func() { <-documentSlots }()
	case <-waitContext.Done():
		if ctx.Err() != nil {
			return report, 0, ctx.Err()
		}
		return report, 0, &DocumentError{"busy"}
	}
	python, script, tempRoot, err := documentPaths()
	if err != nil {
		return report, 0, err
	}
	temporary, err := executiontemp.Create(filepath.Join(tempRoot, "managed-v1"))
	if err != nil {
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	defer temporary.Close()
	folder := temporary.Path()
	source := filepath.Join(folder, "source.bin")
	if err = os.WriteFile(source, data, 0600); err != nil {
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, python, "-B", "-I", script, "--source", source, "--extension", strings.ToLower(filepath.Ext(name)), "--ocr", mode, "--phase", phase, "--page", strconv.Itoa(page))
	// Do not pass provider keys, database credentials, user Python paths or proxy configuration.
	for _, key := range []string{"SystemRoot", "WINDIR", "SYSTEMDRIVE", "TEMP", "TMP"} {
		if value := os.Getenv(key); value != "" {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	command.Env = append(command.Env, "OMP_NUM_THREADS=2", "OPENBLAS_NUM_THREADS=2")
	command.Dir = folder
	configureDocumentProcess(command)
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = io.Discard
	if err = runDocumentCommand(bounded, command); err != nil {
		if ctx.Err() != nil {
			return report, 0, ctx.Err()
		}
		if bounded.Err() != nil {
			return report, 0, &DocumentError{"file_timeout"}
		}
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	var response struct {
		Parser string       `json:"parser"`
		Pages  int          `json:"pages,omitempty"`
		Units  []ImportUnit `json:"units"`
		Error  string       `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	if response.Error != "" {
		return report, 0, &DocumentError{response.Error}
	}
	if phase == "plan" {
		if response.Parser != DocumentParser || response.Pages < 1 || response.Pages > 100 || len(response.Units) != 0 {
			return report, 0, &DocumentError{"runtime_unavailable"}
		}
		report.Parser = response.Parser
		return report, response.Pages, nil
	}
	if response.Pages != 0 || (phase == "unit" && len(response.Units) != 1) {
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	if len(response.Units) == 0 || len(response.Units) > 100 || response.Parser != DocumentParser {
		return report, 0, &DocumentError{"runtime_unavailable"}
	}
	total := 0
	for index := range response.Units {
		unit := &response.Units[index]
		expected := "image:1"
		if strings.EqualFold(filepath.Ext(name), ".pdf") {
			offset := 0
			if phase == "unit" {
				offset = page
			}
			expected = "page:" + strconv.Itoa(index+offset+1)
		} else if index > 0 {
			return report, 0, &DocumentError{"runtime_unavailable"}
		}
		if unit.Locator != expected || (unit.Status != "extracted" && unit.Status != "pending") || (unit.Status == "extracted" && strings.TrimSpace(unit.Text) == "") {
			return report, 0, &DocumentError{"runtime_unavailable"}
		}
		total += len(unit.Text)
		if total > maxExtracted {
			return report, 0, &DocumentError{"text_limit"}
		}
		if unit.Confidence != nil && (*unit.Confidence < 0 || *unit.Confidence > 1) {
			return report, 0, &DocumentError{"runtime_unavailable"}
		}
		if unit.Reason != "" {
			unit.Reason = documentReason(unit.Reason)
		}
		if unit.Kind == "ocr" && unit.Status == "extracted" {
			unit.Reason = "OCR 文字仅供核对；不代表图像内容理解或事实确认"
		}
	}
	report.Parser = response.Parser
	report.Units = response.Units
	return report, 0, nil
}
