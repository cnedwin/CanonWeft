package novel

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestNovelPDFChinesePagination(t *testing.T) {
	b := ExportBook{ID: "pdf-proof", Title: "雾城来信：雨夜与旧案", Author: "测试作者", Language: "zh-Hans", Description: "这是一部中文小说。保留简体、繁體與英文 mixed text。", Notice: "作者私人稿，未完成卷审。", Chapters: []ExportChapter{{Volume: "第一卷 雨夜", Title: "第一章 门外的信", Text: strings.Repeat("林舟停在药铺门外，听见雨滴落进铜盆。\n“你为什么回来？”她问。林舟没有回答，只把信放在桌上。\n", 90) + "首章终点甲。"}, {Volume: "第二卷 旧城", Title: "第二章 灯下的答案", Text: "第二章起点乙。\n他打开信。信上写着 <script> & 42，不是命令。\n" + strings.Repeat("KeepThisLongEnglishWordIntactWherePossible", 12) + "\n全书终点丙。"}}}
	data, e := b.PDF(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) || !bytes.Contains(data, []byte("/FontFile2")) || !bytes.Contains(data, []byte("/ToUnicode")) {
		t.Fatal("not a portable searchable PDF with embedded font")
	}
	if file := os.Getenv("STUDIO_PDF_PROOF"); file != "" {
		if e = os.WriteFile(file, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	b.Chapters[0].Text = "缺字\U0010ffff"
	if _, e = b.PDF(context.Background()); e == nil {
		t.Fatal("unsupported glyph silently disappeared")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = b.PDF(canceled); e == nil {
		t.Fatal("canceled export continued")
	}
}

func TestNovelPDFLongManuscript(t *testing.T) {
	b := ExportBook{ID: "long-pdf", Title: "长篇排版验证", Author: "测试作者", Language: "zh-Hans", Description: "120章中文稿，核对所有章节与正文，不使用模型。"}
	for i := 1; i <= 120; i++ {
		b.Chapters = append(b.Chapters, ExportChapter{Volume: fmt.Sprintf("第%d卷", (i-1)/30+1), Title: fmt.Sprintf("第%d章 河堤寻人", i), Text: fmt.Sprintf("章节起点%03d。", i) + strings.Repeat("他从城门出发，沿河堤寻找当年的证人。", 125) + fmt.Sprintf("章节终点%03d。", i)})
	}
	data, e := b.PDF(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if file := os.Getenv("STUDIO_PDF_LONG_PROOF"); file != "" {
		if e = os.WriteFile(file, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("120 chapters rendered locally, %d bytes", len(data))
}
