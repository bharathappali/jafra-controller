package admission

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMethodTraceSedRewritesOnlyMethodTrace(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not found")
	}
	if _, err := exec.LookPath("sed"); err != nil {
		t.Skip("sed not found")
	}

	fixture, err := os.ReadFile("testdata/methodtrace-fixture.jfc")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	rewritten := runMethodTraceSed(t, fixture)
	before := strings.Split(string(fixture), "\n")
	after := strings.Split(string(rewritten), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count = %d, fixture %d", len(after), len(before))
	}
	diffs := 0
	for index := range before {
		if before[index] == after[index] {
			continue
		}
		diffs++
		if before[index] != `    <setting name="threshold">500 ms</setting>` ||
			after[index] != `    <setting name="threshold">250 ms</setting>` {
			t.Fatalf("unexpected changed line %q -> %q", before[index], after[index])
		}
	}
	if diffs != 1 {
		t.Fatalf("changed lines = %d, want 1", diffs)
	}
	if strings.Count(string(rewritten), `<setting name="threshold">500 ms</setting>`) != 2 {
		t.Fatalf("Compilation and SocketRead thresholds changed:\n%s", rewritten)
	}
	if eventBlock(string(fixture), "jdk.MethodTiming") != eventBlock(string(rewritten), "jdk.MethodTiming") {
		t.Fatal("jdk.MethodTiming block changed")
	}
	trace := eventBlock(string(rewritten), "jdk.MethodTrace")
	if !strings.Contains(trace, `<setting name="threshold">250 ms</setting>`) {
		t.Fatalf("MethodTrace block = %s", trace)
	}
	if !methodTraceThresholdMatches(t, rewritten) {
		t.Fatal("verification did not find the rewritten MethodTrace threshold")
	}

	withoutTrace := strings.Replace(string(fixture), eventBlock(string(fixture), "jdk.MethodTrace")+"\n", "", 1)
	if withoutTrace == string(fixture) {
		t.Fatal("failed to delete the MethodTrace block")
	}
	copied := runMethodTraceSed(t, []byte(withoutTrace))
	if string(copied) != withoutTrace {
		t.Fatalf("missing MethodTrace block was modified:\n%s", copied)
	}
	if methodTraceThresholdMatches(t, copied) {
		t.Fatal("verification succeeded after the MethodTrace block was deleted")
	}
}

func runMethodTraceSed(t *testing.T, input []byte) string {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.jfc")
	target := filepath.Join(dir, "target.jfc")
	if err := os.WriteFile(source, input, 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	script := `sed '` + methodTraceSedProgram + `' "$1" > "$2"`
	cmd := exec.Command("sh", "-c", script, "sh", source, target)
	cmd.Env = append(os.Environ(), "JAFRA_LATENCY_THRESHOLD=250 ms")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sed: %v\n%s", err, output)
	}
	rewritten, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	return string(rewritten)
}

func methodTraceThresholdMatches(t *testing.T, contents string) bool {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "checked.jfc")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write checked file: %v", err)
	}
	script := `sed -n '/<event name="jdk.MethodTrace">/,/<\/event>/p' "$1" | grep -F -q "<setting name=\"threshold\">${JAFRA_LATENCY_THRESHOLD}</setting>"`
	cmd := exec.Command("sh", "-c", script, "sh", path)
	cmd.Env = append(os.Environ(), "JAFRA_LATENCY_THRESHOLD=250 ms")
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	t.Fatalf("verification command: %v", err)
	return false
}

func eventBlock(xml, name string) string {
	startMarker := `<event name="` + name + `">`
	start := strings.Index(xml, startMarker)
	if start < 0 {
		return ""
	}
	rest := xml[start:]
	end := strings.Index(rest, "</event>")
	if end < 0 {
		return ""
	}
	return rest[:end+len("</event>")]
}
