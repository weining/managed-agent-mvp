package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestSandboxTools is an integration test that exercises all new/modified SDK methods
// against the real sandbox. Run with:
//
//	go test -v -run TestSandboxTools -timeout 120s
func TestSandboxTools(t *testing.T) {
	cfg, err := LoadConfig("config.yaml")
	if err != nil || cfg.SandboxBaseURL == "" {
		t.Skip("config.yaml not found or sandbox_base_url not set; skipping integration tests")
	}

	sbx := NewSDKSandboxClient(cfg.SandboxBaseURL, cfg.SandboxID)
	if err := sbx.Init(); err != nil {
		t.Fatalf("sandbox init failed: %v", err)
	}
	t.Logf("✓ Sandbox connected: %s", cfg.SandboxBaseURL)

	// Track results for summary
	type result struct {
		name   string
		passed bool
		detail string
	}
	var results []result

	pass := func(name, detail string) {
		results = append(results, result{name, true, detail})
		t.Logf("✓ PASS  %-35s  %s", name, detail)
	}
	fail := func(name string, err error) {
		results = append(results, result{name, false, err.Error()})
		t.Errorf("✗ FAIL  %-35s  %v", name, err)
	}

	// ─── File operations ──────────────────────────────────────────────────────

	t.Log("\n── FileListPath ──────────────────────────────────────────────────")
	if out, err := sbx.FileListPath("/home", false, false, nil); err != nil {
		fail("FileListPath /home", err)
	} else if out == "" {
		fail("FileListPath /home", fmt.Errorf("empty response"))
	} else {
		pass("FileListPath /home", fmt.Sprintf("%d chars", len(out)))
	}

	t.Log("\n── FileStrReplaceEditor ─────────────────────────────────────────────")
	testFile := "/tmp/_test_file_edit.txt"

	// create
	if out, err := sbx.FileStrReplaceEditor("create", testFile, map[string]interface{}{
		"file_text": "line one\nline two\nline three\n",
	}); err != nil {
		fail("FileStrReplaceEditor create", err)
	} else {
		pass("FileStrReplaceEditor create", out)
	}

	// view
	if out, err := sbx.FileStrReplaceEditor("view", testFile, nil); err != nil {
		fail("FileStrReplaceEditor view", err)
	} else if !strings.Contains(out, "line one") {
		fail("FileStrReplaceEditor view", fmt.Errorf("expected 'line one' in output, got: %s", out))
	} else {
		pass("FileStrReplaceEditor view", "contains expected content")
	}

	// view range
	if out, err := sbx.FileStrReplaceEditor("view", testFile, map[string]interface{}{
		"view_range": []interface{}{float64(1), float64(2)},
	}); err != nil {
		fail("FileStrReplaceEditor view range", err)
	} else {
		pass("FileStrReplaceEditor view range", fmt.Sprintf("%d chars", len(out)))
	}

	// str_replace
	if out, err := sbx.FileStrReplaceEditor("str_replace", testFile, map[string]interface{}{
		"old_str": "line two",
		"new_str": "line TWO (edited)",
	}); err != nil {
		fail("FileStrReplaceEditor str_replace", err)
	} else {
		pass("FileStrReplaceEditor str_replace", out)
	}

	// verify replacement
	if content, err := sbx.ReadFile(testFile); err != nil {
		fail("FileStrReplaceEditor verify", err)
	} else if !strings.Contains(content, "line TWO (edited)") {
		fail("FileStrReplaceEditor verify", fmt.Errorf("replacement not found: %s", content))
	} else {
		pass("FileStrReplaceEditor verify", "replacement confirmed")
	}

	// insert
	if out, err := sbx.FileStrReplaceEditor("insert", testFile, map[string]interface{}{
		"insert_line": float64(1),
		"new_str":     "inserted after line 1",
	}); err != nil {
		fail("FileStrReplaceEditor insert", err)
	} else {
		pass("FileStrReplaceEditor insert", out)
	}

	// undo_edit
	if out, err := sbx.FileStrReplaceEditor("undo_edit", testFile, nil); err != nil {
		fail("FileStrReplaceEditor undo_edit", err)
	} else {
		pass("FileStrReplaceEditor undo_edit", out)
	}

	// cleanup
	sbx.ExecCommand("rm -f " + testFile)

	// ─── Browser: navigate + content extraction ───────────────────────────────

	t.Log("\n── BrowserNavigate ──────────────────────────────────────────────────")
	if out, err := sbx.BrowserNavigate("https://example.com"); err != nil {
		fail("BrowserNavigate", err)
	} else {
		pass("BrowserNavigate", out)
	}

	t.Log("\n── BrowseWebSDK (Navigate+GetMarkdown) ──────────────────────────────")
	if out, err := sbx.BrowseWebSDK("https://example.com"); err != nil {
		fail("BrowseWebSDK", err)
	} else if len(out) < 50 {
		fail("BrowseWebSDK", fmt.Errorf("too short (%d chars): %s", len(out), out))
	} else {
		pass("BrowseWebSDK", fmt.Sprintf("%d chars, first 80: %s", len(out), truncate(out, 80)))
	}

	t.Log("\n── TakeScreenshotSDK ────────────────────────────────────────────────")
	if out, err := sbx.TakeScreenshotSDK(""); err != nil { // screenshot current page
		fail("TakeScreenshotSDK", err)
	} else if !strings.Contains(out, "Screenshot saved") {
		fail("TakeScreenshotSDK", fmt.Errorf("unexpected output: %s", out))
	} else {
		pass("TakeScreenshotSDK", out)
	}

	// ─── Browser: interaction ─────────────────────────────────────────────────

	t.Log("\n── BrowserGetElements ───────────────────────────────────────────────")
	if out, err := sbx.BrowserGetElements(); err != nil {
		fail("BrowserGetElements", err)
	} else {
		pass("BrowserGetElements", fmt.Sprintf("%d chars", len(out)))
	}

	t.Log("\n── BrowserEvaluate ──────────────────────────────────────────────────")
	if out, err := sbx.BrowserEvaluate("document.title"); err != nil {
		fail("BrowserEvaluate", err)
	} else {
		pass("BrowserEvaluate", fmt.Sprintf("title=%q", out))
	}

	t.Log("\n── BrowserScroll ────────────────────────────────────────────────────")
	if out, err := sbx.BrowserScroll("down", nil); err != nil {
		fail("BrowserScroll", err)
	} else {
		pass("BrowserScroll", out)
	}

	t.Log("\n── BrowserGetConsole ────────────────────────────────────────────────")
	if out, err := sbx.BrowserGetConsole(); err != nil {
		fail("BrowserGetConsole", err)
	} else {
		pass("BrowserGetConsole", fmt.Sprintf("%d chars", len(out)))
	}

	// Navigate to a form page to test fill/type_text/press_key
	t.Log("\n── BrowserNavigate to form page ─────────────────────────────────────")
	sbx.BrowserNavigate("https://www.bing.com/search?q=test")

	t.Log("\n── BrowserFill (Bing search box) ────────────────────────────────────")
	if out, err := sbx.BrowserFill("", nil, "sandbox sdk test"); err != nil {
		// Bing might not have a focused input; try with selector
		if out2, err2 := sbx.BrowserFill("input[name=q]", nil, "sandbox sdk test"); err2 != nil {
			fail("BrowserFill", fmt.Errorf("no-selector: %v; with-selector: %v", err, err2))
		} else {
			pass("BrowserFill (selector)", out2)
		}
	} else {
		pass("BrowserFill (no selector)", out)
	}

	t.Log("\n── BrowserTypeText ──────────────────────────────────────────────────")
	// Navigate to a simple page first
	sbx.BrowserNavigate("https://example.com")
	if out, err := sbx.BrowserTypeText("hello", nil); err != nil {
		fail("BrowserTypeText", err)
	} else {
		pass("BrowserTypeText", out)
	}

	t.Log("\n── BrowserPressKey ──────────────────────────────────────────────────")
	if out, err := sbx.BrowserPressKey("Tab"); err != nil {
		fail("BrowserPressKey", err)
	} else {
		pass("BrowserPressKey", out)
	}

	t.Log("\n── UploadFile ───────────────────────────────────────────────────────")
	uploadContent := strings.NewReader("hello upload test\n")
	if uploadedPath, err := sbx.UploadFile(uploadContent, "/tmp/_test_upload.txt"); err != nil {
		fail("UploadFile", err)
	} else {
		// Verify by reading back
		content, readErr := sbx.ReadFile(uploadedPath)
		if readErr != nil {
			fail("UploadFile read-back", readErr)
		} else if !strings.Contains(content, "hello upload test") {
			fail("UploadFile read-back", fmt.Errorf("unexpected content: %s", content))
		} else {
			pass("UploadFile", fmt.Sprintf("path=%s content-verified", uploadedPath))
		}
	}

	t.Log("\n── BrowserClick (by coordinates) ────────────────────────────────────")
	x, y := 400.0, 300.0
	if out, err := sbx.BrowserClick("", nil, &x, &y); err != nil {
		fail("BrowserClick coords", err)
	} else {
		pass("BrowserClick coords", out)
	}

	// ─── Summary ─────────────────────────────────────────────────────────────

	t.Log("\n════════════════════════════════════════════════════════════════════")
	passed, failed := 0, 0
	for _, r := range results {
		if r.passed {
			passed++
		} else {
			failed++
		}
	}
	t.Logf("SUMMARY: %d passed, %d failed (total %d)", passed, failed, len(results))
	if failed > 0 {
		t.Log("\nFailed tests:")
		for _, r := range results {
			if !r.passed {
				t.Logf("  ✗ %s: %s", r.name, r.detail)
			}
		}
	}
}

// TestSandboxBasic tests the core operations that should always work.
func TestSandboxBasic(t *testing.T) {
	cfg, err := LoadConfig("config.yaml")
	if err != nil || cfg.SandboxBaseURL == "" {
		t.Skip("sandbox not configured")
	}
	sbx := NewSDKSandboxClient(cfg.SandboxBaseURL, cfg.SandboxID)
	if err := sbx.Init(); err != nil {
		t.Fatalf("sandbox init: %v", err)
	}

	// ExecCommand
	out, code, err := sbx.ExecCommand("echo hello")
	if err != nil || code != 0 || !strings.Contains(out, "hello") {
		t.Errorf("ExecCommand: code=%d out=%q err=%v", code, out, err)
	} else {
		t.Logf("✓ ExecCommand: %q", strings.TrimSpace(out))
	}

	// WriteFile / ReadFile
	if err := sbx.WriteFile("/tmp/_test_rw.txt", "rw test content"); err != nil {
		t.Errorf("WriteFile: %v", err)
	}
	content, err := sbx.ReadFile("/tmp/_test_rw.txt")
	if err != nil || !strings.Contains(content, "rw test content") {
		t.Errorf("ReadFile: content=%q err=%v", content, err)
	} else {
		t.Logf("✓ WriteFile/ReadFile: %q", content)
	}
	sbx.ExecCommand("rm -f /tmp/_test_rw.txt")

	// FileListPath
	out2, err := sbx.FileListPath("/tmp", false, false, nil)
	if err != nil {
		t.Errorf("FileListPath: %v", err)
	} else {
		t.Logf("✓ FileListPath: %d chars", len(out2))
	}

	// FileStrReplaceEditor view on a known file
	out3, err := sbx.FileStrReplaceEditor("view", "/etc/hostname", nil)
	if err != nil {
		t.Errorf("FileStrReplaceEditor view /etc/hostname: %v", err)
	} else {
		t.Logf("✓ FileStrReplaceEditor view: %q", truncate(out3, 100))
	}

	// ExecuteCode (Python)
	out4, err := sbx.ExecuteCode("python", "print('hello from python')", "", 10)
	if err != nil || !strings.Contains(out4, "hello from python") {
		t.Errorf("ExecuteCode python: out=%q err=%v", out4, err)
	} else {
		t.Logf("✓ ExecuteCode python: %q", truncate(out4, 80))
	}

	// FileGrep
	out5, err := sbx.FileGrep("/etc", "hostname", nil, nil, false, 5)
	if err != nil {
		t.Errorf("FileGrep: %v", err)
	} else {
		t.Logf("✓ FileGrep: %q", truncate(out5, 80))
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// TestMain allows running with -test.v to see all output.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
