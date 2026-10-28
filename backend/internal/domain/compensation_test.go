package domain

import (
	"errors"
	"testing"
)

func TestTextCompensationPreservesLaterEditsAcrossVersionChain(t *testing.T) {
	before, published := "序章🌙：旧城。\n保留结尾。", "序章🌙：新城。\n保留结尾。"
	edit, err := PlanTextCompensation(before, published)
	if err != nil {
		t.Fatal(err)
	}
	later := "前言\n" + published
	edit, err = RebaseTextCompensation(edit, published, later)
	if err != nil {
		t.Fatal(err)
	}
	latest := "前言\n序章🌙：新城。\n另写的结尾。"
	edit, err = RebaseTextCompensation(edit, later, latest)
	if err != nil {
		t.Fatal(err)
	}
	runes := []rune(latest)
	result := string(runes[:edit.StartRune]) + edit.Replacement + string(runes[edit.EndRune:])
	if result != "前言\n序章🌙：旧城。\n另写的结尾。" {
		t.Fatal(result)
	}
}

func TestTextCompensationRefusesOverlapsAndInvalidAnchors(t *testing.T) {
	edit, err := PlanTextCompensation("甲旧城乙", "甲新城乙")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RebaseTextCompensation(edit, "甲新城乙", "甲古城乙"); !errors.Is(err, ErrConflict) {
		t.Fatal("overlapping edit was overwritten", err)
	}
	if _, err = RebaseTextCompensation(edit, "甲古城乙", "甲古城乙丙"); !errors.Is(err, ErrNotReady) {
		t.Fatal("bad anchor accepted", err)
	}
	if _, err = PlanTextCompensation("同文", "同文"); !errors.Is(err, ErrValidation) {
		t.Fatal("no-op accepted", err)
	}
}

func TestTextCompensationHandlesInsertDeleteAndAmbiguousBoundary(t *testing.T) {
	for _, pair := range [][2]string{{"甲乙", "甲新增乙"}, {"甲删除乙", "甲乙"}, {"", "新增正文"}, {"删掉全文", ""}} {
		edit, err := PlanTextCompensation(pair[0], pair[1])
		if err != nil {
			t.Fatal(err)
		}
		runes := []rune(pair[1])
		if result := string(runes[:edit.StartRune]) + edit.Replacement + string(runes[edit.EndRune:]); result != pair[0] {
			t.Fatal(pair, result)
		}
	}
	edit, _ := PlanTextCompensation("甲删除乙", "甲乙")
	if _, err := RebaseTextCompensation(edit, "甲乙", "甲其他乙"); !errors.Is(err, ErrConflict) {
		t.Fatal("same insertion boundary accepted", err)
	}
}
