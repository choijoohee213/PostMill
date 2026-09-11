package main

import "testing"

func TestSplitDraft_구분자로_나눈다(t *testing.T) {
	body, detail := splitDraft("첫 줄\n둘째 줄\n---\n디테일 첫 줄\n디테일 둘째 줄")
	if body != "첫 줄\n둘째 줄" {
		t.Errorf("body=%q", body)
	}
	if detail != "디테일 첫 줄\n디테일 둘째 줄" {
		t.Errorf("detail=%q", detail)
	}
}

func TestSplitDraft_구분자가_없으면_전부_본문이다(t *testing.T) {
	// 디테일 없이 발행할 수 있어야 하므로 실패시키지 않는다.
	body, detail := splitDraft("본문만 있다")
	if body != "본문만 있다" || detail != "" {
		t.Fatalf("body=%q detail=%q", body, detail)
	}
}

func TestSplitDraft_구분자_앞뒤_공백을_정리한다(t *testing.T) {
	body, detail := splitDraft("\n\n  본문  \n\n  ---  \n\n  디테일  \n\n")
	if body != "본문" || detail != "디테일" {
		t.Fatalf("body=%q detail=%q", body, detail)
	}
}

func TestSplitDraft_본문에_있는_대시는_구분자가_아니다(t *testing.T) {
	// 구분자는 그 줄에 --- 만 있을 때다.
	body, detail := splitDraft("가격이 3--- 만원\n---\n디테일")
	if body != "가격이 3--- 만원" || detail != "디테일" {
		t.Fatalf("body=%q detail=%q", body, detail)
	}
}
