package publish

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalIssueBodyByteBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		d := draft
		d.ReportID, d.Body = "boundary", ""
		overhead := len(issueBody(d))
		d.Body = strings.Repeat("é", (60*1024-overhead)/2)
		d.Body += strings.Repeat("x", 60*1024-len(issueBody(d))+extra)
		f := newFake()
		_, err := Submit(context.Background(), f.exec, t.TempDir(), d)
		if extra == 0 {
			if err != nil || f.in(create) != issueBody(d) || len(f.in(create)) != 60*1024 {
				t.Fatalf("exact limit: %v, sent %d bytes", err, len(f.in(create)))
			}
		} else if err == nil || !strings.Contains(err.Error(), "Review") || len(f.calls) != 0 {
			t.Fatalf("oversize must fail before remote work: %v %v", err, f.calls)
		}
	}
}

func TestAccountSwitchKeepsUncertainRecovery(t *testing.T) {
	for _, gist := range []bool{false, true} {
		f, dir := newFake(), t.TempDir()
		d := draft
		d.ReportID = "account-recovery"
		if gist {
			f.fail["artifact create"] = true
			f.errs = map[string]string{"artifact create": "HTTP 403"}
			f.fail["gist create"] = true
		} else {
			f.fail["issue create"] = true
		}
		if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
			t.Fatal("expected uncertain write")
		}
		s, err := load(filepath.Join(dir, d.ReportID, "state.json"))
		if err != nil || s.Account != "me" || s.Stage != Uncertain {
			t.Fatalf("account not bound before write: %+v %v", s, err)
		}
		f.calls = nil
		f.out["api user"] = "other"
		if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err == nil || !strings.Contains(err.Error(), "gh auth switch") {
			t.Fatalf("expected actionable account mismatch: %v", err)
		}
		if len(f.calls) != 2 || f.count("auth status") != 1 || f.count("api user") != 1 {
			t.Fatal("mismatch did remote work", f.calls)
		}
		f.calls = nil
		f.out["api user"] = "me"
		f.fail["issue create"], f.fail["gist create"] = false, false
		if gist {
			f.out["api gists"] = "https://gist.github.com/recovered"
		} else {
			f.out["api issues"] = "https://github.com/projectbluefin/dakota/issues/9"
			f.out["issue view"] = "kind/bug"
		}
		if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err != nil {
			t.Fatal(err)
		}
		if f.count(create) != 0 || f.count("gist create") != 0 {
			t.Fatal("recovery duplicated an accepted write", f.calls)
		}
		s, err = load(filepath.Join(dir, d.ReportID, "state.json"))
		if err != nil || s.ReportID != d.ReportID || s.Account != "me" || s.Stage != Published {
			t.Fatalf("lost recovery identity: %+v %v", s, err)
		}
	}
}

func TestOversizedResumeRefusedBeforeRemoteWork(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.ReportID, d.Body = "oversized-resume", strings.Repeat("x", 60*1024)
	if err := save(dir, &Saved{ReportID: d.ReportID, Stage: Reviewed, Draft: d}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err == nil || !strings.Contains(err.Error(), "Review") {
		t.Fatalf("oversized resume accepted: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("oversized resume caused remote work", f.calls)
	}
}

func TestLegacyUnboundUncertainRefused(t *testing.T) {
	for _, stage := range []Stage{Uncertain, IssuePending, GistPending} {
		f, dir := newFake(), t.TempDir()
		d := draft
		d.ReportID = "legacy"
		s := &Saved{ReportID: d.ReportID, Draft: d, Stage: stage}
		if err := save(dir, s); err != nil {
			t.Fatal(err)
		}
		if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err == nil || !strings.Contains(err.Error(), "original account") {
			t.Fatalf("legacy uncertainty silently adopted: %v", err)
		}
		if len(f.calls) != 2 {
			t.Fatal("legacy uncertainty caused remote work", f.calls)
		}
		got, err := load(filepath.Join(dir, d.ReportID, "state.json"))
		if err != nil || got.Account != "" || got.Stage != stage {
			t.Fatalf("legacy recovery mutated: %+v %v", got, err)
		}
	}
}

func TestEmptyAuthenticatedAccountRefused(t *testing.T) {
	f := newFake()
	f.out["api user"] = "\n"
	if _, err := Submit(context.Background(), f.exec, t.TempDir(), draft); err == nil || !strings.Contains(err.Error(), "empty login") {
		t.Fatalf("empty identity accepted: %v", err)
	}
	if f.count(create) != 0 {
		t.Fatal("wrote without an account")
	}
}

func TestUpdateResumeArtifactAndComment(t *testing.T) {
	if NewIssue != 0 || Confirm != 1 || Update != 2 {
		t.Fatal("saved mode values changed")
	}
	f, dir := newFake(), t.TempDir()
	d := draft
	d.Mode, d.IssueRef, d.ReportID = Update, "#3", "update-report"
	f.out["api issues"] = "ME\n" // the signed-in account opened the issue
	f.fail["artifact create"] = true
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("expected interrupted artifact upload")
	}
	f.fail["artifact create"] = false
	f.out["artifact list"] = "systemd-report-update-report.json\n"
	f.fail["issue comment"] = true
	if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err == nil {
		t.Fatal("expected interrupted comment")
	}
	body := f.in("issue comment")
	if !strings.Contains(body, "<!-- bluefin-report-update -->") || strings.Contains(body, "bluefin-report-confirm") || !strings.Contains(body, "systemd-report-update-report.json") {
		t.Fatal(body)
	}
	f.fail["issue comment"] = false
	f.out["api comments"] = "123"
	if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err != nil {
		t.Fatal(err)
	}
	if f.count(create) != 0 || f.count("gist create") != 0 || f.count("issue artifact create") != 1 || f.count("issue comment") != 1 {
		t.Fatal("update duplicated writes", f.calls)
	}
	if p, err := Pending(dir); err != nil || len(p) != 0 {
		t.Fatal(p, err)
	}
}

func TestUpdateRefusedForOtherReporter(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	f.out["api issues"] = "someone-else\n"
	d := draft
	d.Mode, d.IssueRef, d.ReportID = Update, "3", "foreign-update"
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil || !strings.Contains(err.Error(), "Confirm Existing Issue") {
		t.Fatalf("update on another reporter's issue accepted: %v", err)
	}
	if f.count("issue") != 0 {
		t.Fatal("wrote to another reporter's issue", f.calls)
	}
	if p, err := Pending(dir); err != nil || len(p) != 0 {
		t.Fatal("refused update left a draft", p, err)
	}
}

func TestUnknownPublishModeRefused(t *testing.T) {
	f, dir := newFake(), t.TempDir()
	d := draft
	d.Mode, d.ReportID = Mode(3), "unknown"
	if _, err := Submit(context.Background(), f.exec, dir, d); err == nil {
		t.Fatal("unknown mode submitted")
	}
	if err := save(dir, &Saved{ReportID: d.ReportID, Stage: Reviewed, Draft: d}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(context.Background(), f.exec, dir, d.ReportID); err == nil {
		t.Fatal("unknown mode resumed")
	}
	if len(f.calls) != 0 {
		t.Fatal("unknown mode caused remote work", f.calls)
	}
}
