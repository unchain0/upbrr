// Copyright (c) 2025-2026, Audionut and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package mediainfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	preparationstate "github.com/autobrr/upbrr/internal/preparedrelease/state"

	paths "github.com/autobrr/upbrr/internal/pathing/layout"
	"github.com/autobrr/upbrr/pkg/api"
)

func TestExportWritesCleanedArtifacts(t *testing.T) {
	tmpRoot := t.TempDir()
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "Movie.Title.mkv")
	if err := os.WriteFile(targetPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	textOutput := strings.Join([]string{
		"General",
		"ReportBy: MediaInfo",
		"Report created by MediaInfo",
		"Complete name                            : " + filepath.Join(targetDir, "rendered", filepath.Base(targetPath)),
		"Source                                   : " + targetPath,
	}, "\n")
	jsonOutput := "{\"media\":{\"track\":[{\"@type\":\"General\"}]}}"

	analyzer := &fakeAnalyzer{text: textOutput, json: jsonOutput}
	service := NewService(api.NopLogger{}, analyzer)

	result, err := service.Export(context.Background(), Request{
		SourcePath: targetPath,
		VideoPath:  targetPath,
		TempRoot:   tmpRoot,
		Release: api.ReleaseInfo{
			Title: "Movie.Title",
			Year:  2024,
		},
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	textData, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read text: %v", err)
	}
	text := string(textData)
	if strings.Contains(text, "ReportBy") {
		t.Fatalf("expected ReportBy lines removed")
	}
	if strings.Contains(text, targetDir) {
		t.Fatalf("expected host paths to be cleaned, got %q", text)
	}
	wantCompleteName := "Complete name                            : " + filepath.Base(targetPath)
	if !strings.Contains(text, wantCompleteName) {
		t.Fatalf("expected normalized complete name %q, got %q", wantCompleteName, text)
	}
	if !strings.Contains(text, "Source                                   : "+filepath.Base(targetPath)) {
		t.Fatalf("expected exact target occurrences to use the basename, got %q", text)
	}

	jsonData, err := os.ReadFile(result.JSONPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	if string(jsonData) != jsonOutput {
		t.Fatalf("unexpected json output: %s", string(jsonData))
	}
}

func TestExportReusesExistingArtifactsWhenConformanceOK(t *testing.T) {
	tmpRoot := t.TempDir()
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "Movie.Title.mkv")
	if err := os.WriteFile(targetPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	evidence := fullSourceFileForTest(t, targetPath)

	release := api.ReleaseInfo{Title: "Movie.Title", Year: 2024}
	tmpDir, _, err := paths.ReleaseTempDir(tmpRoot, preparationstate.State{Release: release}, targetPath)
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	tmpDir = filepath.Join(tmpDir, "mediainfo-"+sourceNamespace(targetPath, evidence.SHA256))
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("create MediaInfo dir: %v", err)
	}

	textPath := filepath.Join(tmpDir, "mediainfo.txt")
	jsonPath := filepath.Join(tmpDir, "MediaInfo.json")
	cachedText := strings.Join([]string{
		"General",
		"Complete name                            : " + filepath.Join(targetDir, "stale", filepath.Base(targetPath)),
		"Source                                   : " + targetPath,
	}, "\n")
	if err := os.WriteFile(textPath, []byte(cachedText), 0o600); err != nil {
		t.Fatalf("write text: %v", err)
	}
	jsonPayload := "{\"media\":{\"track\":[{\"@type\":\"General\",\"extra\":{\"ConformanceErrors\":{}}}]}}"
	if err := os.WriteFile(jsonPath, []byte(jsonPayload), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	writeSourceEvidenceForTest(t, tmpDir, evidence)

	service := NewService(api.NopLogger{}, panicAnalyzer{t: t})
	result, err := service.Export(context.Background(), Request{
		SourcePath:   targetPath,
		VideoPath:    targetPath,
		TempRoot:     tmpRoot,
		FullEvidence: []api.FullSourceFile{evidence},
		Release:      release,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if result.TextPath != textPath {
		t.Fatalf("unexpected text path: %s", result.TextPath)
	}
	if result.JSONPath != jsonPath {
		t.Fatalf("unexpected json path: %s", result.JSONPath)
	}
	textData, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read reused text: %v", err)
	}
	text := string(textData)
	if strings.Contains(text, targetDir) {
		t.Fatalf("expected cached host paths to be cleaned, got %q", text)
	}
	wantCompleteName := "Complete name                            : " + filepath.Base(targetPath)
	if !strings.Contains(text, wantCompleteName) {
		t.Fatalf("expected cached complete name %q, got %q", wantCompleteName, text)
	}
	tempMatches, err := filepath.Glob(filepath.Join(tmpDir, "mediainfo.txt.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(tempMatches) != 0 {
		t.Fatalf("expected atomic cache replacement to remove temp files, got %v", tempMatches)
	}
}

func TestExportReusesOnlyArtifactsBoundToCurrentSourceBytes(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "Movie.Title.mkv")
	prefix := bytes.Repeat([]byte("a"), int(api.SourceContentIdentitySampleBytes))
	firstBytes := append(append([]byte(nil), prefix...), []byte("first-tail")...)
	if err := os.WriteFile(targetPath, firstBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	analyzer := &fakeAnalyzer{text: "General", json: `{"source":"first"}`}
	service := NewService(api.NopLogger{}, analyzer)
	evidence := fullSourceFileForTest(t, targetPath)
	req := Request{
		SourcePath:   targetPath,
		VideoPath:    targetPath,
		TempRoot:     t.TempDir(),
		FullEvidence: []api.FullSourceFile{evidence},
	}
	if _, err := service.Export(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Export(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if analyzer.calls != 1 {
		t.Fatalf("stable source analyzed %d times, want one cache reuse", analyzer.calls)
	}

	changedBytes := append(append([]byte(nil), prefix...), []byte("other-tail")...)
	if err := os.WriteFile(targetPath, changedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(targetPath, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("test mutation did not preserve size and mtime")
	}

	evidence = fullSourceFileForTest(t, targetPath)
	req.FullEvidence = []api.FullSourceFile{evidence}
	analyzer.json = `{"source":"other"}`
	result, err := service.Export(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if analyzer.calls != 2 {
		t.Fatalf("same-stat tail mutation reused stale report; analyzer calls = %d", analyzer.calls)
	}
	got, err := os.ReadFile(result.JSONPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != analyzer.json {
		t.Fatalf("MediaInfo report still describes previous source: %s", got)
	}
}

func TestExportSeparatesConcurrentSourcesWithSameBasename(t *testing.T) {
	tmpRoot := t.TempDir()
	firstPath := filepath.Join(t.TempDir(), "Movie.mkv")
	secondPath := filepath.Join(t.TempDir(), "Movie.mkv")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}

	firstStarted := make(chan struct{})
	secondDone := make(chan struct{})
	analyzer := collisionAnalyzer{
		firstPath:    firstPath,
		firstStarted: firstStarted,
		secondDone:   secondDone,
	}
	service := NewService(api.NopLogger{}, analyzer)
	ctx := t.Context()
	export := func(path string, evidence api.FullSourceFile) (Result, error) {
		return service.Export(ctx, Request{
			SourcePath:   path,
			VideoPath:    path,
			TempRoot:     tmpRoot,
			FullEvidence: []api.FullSourceFile{evidence},
		})
	}
	firstEvidence := fullSourceFileForTest(t, firstPath)
	secondEvidence := fullSourceFileForTest(t, secondPath)

	firstResult := make(chan Result, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := export(firstPath, firstEvidence)
		firstResult <- result
		firstErr <- err
	}()
	<-firstStarted
	secondResult, err := export(secondPath, secondEvidence)
	close(secondDone)
	if err != nil {
		t.Fatal(err)
	}
	result := <-firstResult
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if result.JSONPath == secondResult.JSONPath {
		t.Fatalf("same-basename sources shared MediaInfo path %q", result.JSONPath)
	}
	for _, check := range []struct {
		result Result
		want   string
	}{
		{result: result, want: `{"source":"first"}`},
		{result: secondResult, want: `{"source":"second"}`},
	} {
		payload, err := os.ReadFile(check.result.JSONPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != check.want {
			t.Fatalf("report %q = %s, want %s", check.result.JSONPath, payload, check.want)
		}
	}
}

func TestExportSeparatesConcurrentGenerationsOfSamePath(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "Movie.mkv")
	if err := os.WriteFile(targetPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstEvidence := fullSourceFileForTest(t, targetPath)

	analyzer := &generationRaceAnalyzer{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	service := NewService(api.NopLogger{}, analyzer)
	tmpRoot := t.TempDir()
	export := func(evidence api.FullSourceFile) (Result, error) {
		return service.Export(t.Context(), Request{
			SourcePath:   targetPath,
			VideoPath:    targetPath,
			TempRoot:     tmpRoot,
			FullEvidence: []api.FullSourceFile{evidence},
		})
	}

	firstResult := make(chan Result, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := export(firstEvidence)
		firstResult <- result
		firstErr <- err
	}()
	<-analyzer.firstStarted
	if err := os.WriteFile(targetPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondEvidence := fullSourceFileForTest(t, targetPath)
	secondResult, err := export(secondEvidence)
	if err != nil {
		t.Fatal(err)
	}
	close(analyzer.releaseFirst)
	result := <-firstResult
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if result.JSONPath == secondResult.JSONPath {
		t.Fatalf("different generations shared MediaInfo path %q", result.JSONPath)
	}

	restarted := NewService(api.NopLogger{}, panicAnalyzer{t: t})
	for _, check := range []struct {
		evidence api.FullSourceFile
		want     string
	}{
		{evidence: firstEvidence, want: `{"source":"first"}`},
		{evidence: secondEvidence, want: `{"source":"second"}`},
	} {
		reused, err := restarted.Export(t.Context(), Request{
			SourcePath:   targetPath,
			VideoPath:    targetPath,
			TempRoot:     tmpRoot,
			FullEvidence: []api.FullSourceFile{check.evidence},
		})
		if err != nil {
			t.Fatal(err)
		}
		payload, err := os.ReadFile(reused.JSONPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != check.want {
			t.Fatalf("reused report = %s, want %s", payload, check.want)
		}
	}
}

func TestExportSeparatesSequentialArtifactsWithMalformedEvidence(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "Movie.mkv")
	if err := os.WriteFile(targetPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	analyzer := &fakeAnalyzer{text: "General", json: `{"source":"first"}`}
	service := NewService(api.NopLogger{}, analyzer)
	req := Request{
		SourcePath: targetPath,
		VideoPath:  targetPath,
		TempRoot:   t.TempDir(),
		FullEvidence: []api.FullSourceFile{{
			LocalPath: targetPath,
			Size:      5,
			SHA256:    "malformed",
		}},
	}
	first, err := service.Export(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	analyzer.json = `{"source":"second"}`
	second, err := service.Export(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if first.JSONPath == second.JSONPath {
		t.Fatalf("malformed evidence reused MediaInfo path %q", first.JSONPath)
	}
	if analyzer.calls != 2 {
		t.Fatalf("malformed evidence reused artifacts; analyzer calls = %d", analyzer.calls)
	}
	for _, check := range []struct {
		result Result
		want   string
	}{
		{result: first, want: `{"source":"first"}`},
		{result: second, want: `{"source":"second"}`},
	} {
		payload, err := os.ReadFile(check.result.JSONPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != check.want {
			t.Fatalf("report %q = %s, want %s", check.result.JSONPath, payload, check.want)
		}
	}
}

func TestExportSeparatesConcurrentArtifactsWithoutEvidence(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "Movie.mkv")
	if err := os.WriteFile(targetPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}

	analyzer := &generationRaceAnalyzer{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	service := NewService(api.NopLogger{}, analyzer)
	req := Request{
		SourcePath: targetPath,
		VideoPath:  targetPath,
		TempRoot:   t.TempDir(),
	}
	firstResult := make(chan Result, 1)
	firstErr := make(chan error, 1)
	go func() {
		result, err := service.Export(t.Context(), req)
		firstResult <- result
		firstErr <- err
	}()
	<-analyzer.firstStarted
	second, err := service.Export(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	close(analyzer.releaseFirst)
	first := <-firstResult
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	if first.JSONPath == second.JSONPath {
		t.Fatalf("concurrent exports shared MediaInfo path %q", first.JSONPath)
	}
	for _, check := range []struct {
		result Result
		want   string
	}{
		{result: first, want: `{"source":"first"}`},
		{result: second, want: `{"source":"second"}`},
	} {
		payload, err := os.ReadFile(check.result.JSONPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != check.want {
			t.Fatalf("report %q = %s, want %s", check.result.JSONPath, payload, check.want)
		}
	}
}

func TestExportRestrictsUnchangedCachedTextPermissions(t *testing.T) {
	tmpRoot := t.TempDir()
	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "Example.Release.2026.mkv")
	if err := os.WriteFile(targetPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	evidence := fullSourceFileForTest(t, targetPath)

	release := api.ReleaseInfo{Title: "Example Release", Year: 2026}
	tmpDir, _, err := paths.ReleaseTempDir(tmpRoot, preparationstate.State{Release: release}, targetPath)
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	tmpDir = filepath.Join(tmpDir, "mediainfo-"+sourceNamespace(targetPath, evidence.SHA256))
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("create MediaInfo dir: %v", err)
	}

	textPath := filepath.Join(tmpDir, "mediainfo.txt")
	jsonPath := filepath.Join(tmpDir, "MediaInfo.json")
	cleanText := "Complete name : " + filepath.Base(targetPath)
	if err := os.WriteFile(textPath, []byte(cleanText), 0o600); err != nil {
		t.Fatalf("write text: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(textPath, 0o644); err != nil {
			t.Fatalf("relax text permissions: %v", err)
		}
	}
	jsonPayload := "{\"media\":{\"track\":[{\"@type\":\"General\",\"extra\":{\"ConformanceErrors\":{}}}]}}"
	if err := os.WriteFile(jsonPath, []byte(jsonPayload), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	writeSourceEvidenceForTest(t, tmpDir, evidence)

	service := NewService(api.NopLogger{}, panicAnalyzer{t: t})
	result, err := service.Export(context.Background(), Request{
		SourcePath:   targetPath,
		VideoPath:    targetPath,
		TempRoot:     tmpRoot,
		FullEvidence: []api.FullSourceFile{evidence},
		Release:      release,
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	textData, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read reused text: %v", err)
	}
	if string(textData) != cleanText {
		t.Fatalf("expected unchanged cached text %q, got %q", cleanText, textData)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(result.TextPath)
		if err != nil {
			t.Fatalf("stat reused text: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("expected reused text mode 0600, got %#o", got)
		}
	}
}

func TestSelectDVDTargetReturnsMatchingVOBSet(t *testing.T) {
	root := t.TempDir()
	videoTS := filepath.Join(root, "VIDEO_TS")
	if err := os.MkdirAll(videoTS, 0o700); err != nil {
		t.Fatalf("mkdir VIDEO_TS: %v", err)
	}

	if err := os.WriteFile(filepath.Join(videoTS, "VTS_01_0.IFO"), []byte("ifo1"), 0o600); err != nil {
		t.Fatalf("write ifo1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videoTS, "VTS_02_0.IFO"), []byte("ifo2"), 0o600); err != nil {
		t.Fatalf("write ifo2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videoTS, "VTS_01_1.VOB"), []byte(strings.Repeat("a", 100)), 0o600); err != nil {
		t.Fatalf("write vob1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videoTS, "VTS_02_1.VOB"), []byte(strings.Repeat("b", 300)), 0o600); err != nil {
		t.Fatalf("write vob2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(videoTS, "VTS_02_2.VOB"), []byte(strings.Repeat("b", 200)), 0o600); err != nil {
		t.Fatalf("write vob2b: %v", err)
	}

	target, err := selectDVDTarget(context.Background(), root)
	if err != nil {
		t.Fatalf("select dvd target: %v", err)
	}
	if !strings.HasSuffix(strings.ToUpper(target.IFOPath), "VTS_02_0.IFO") {
		t.Fatalf("expected VTS_02_0.IFO, got %s", target.IFOPath)
	}
	if !strings.HasSuffix(strings.ToUpper(target.VOBPath), "VTS_02_1.VOB") {
		t.Fatalf("expected matching VTS_02_1.VOB, got %s", target.VOBPath)
	}
	if target.VOBSet != "02" {
		t.Fatalf("expected set 02, got %q", target.VOBSet)
	}
}

func TestExportDVDAnalyzesIFOAndMatchingVOB(t *testing.T) {
	tmpRoot := t.TempDir()
	root := t.TempDir()
	videoTS := filepath.Join(root, "VIDEO_TS")
	if err := os.MkdirAll(videoTS, 0o700); err != nil {
		t.Fatalf("mkdir VIDEO_TS: %v", err)
	}
	ifoPath := filepath.Join(videoTS, "VTS_01_0.IFO")
	vobPath := filepath.Join(videoTS, "VTS_01_1.VOB")
	if err := os.WriteFile(ifoPath, []byte("ifo"), 0o600); err != nil {
		t.Fatalf("write ifo: %v", err)
	}
	if err := os.WriteFile(vobPath, []byte(strings.Repeat("v", 10)), 0o600); err != nil {
		t.Fatalf("write vob: %v", err)
	}

	analyzer := &recordingAnalyzer{text: "General\nComplete name : X", json: "{\"media\":{\"track\":[{\"@type\":\"General\"}]}}"}
	service := NewService(api.NopLogger{}, analyzer)

	result, err := service.Export(context.Background(), Request{
		SourcePath: root,
		DiscType:   "DVD",
		TempRoot:   tmpRoot,
		Release:    api.ReleaseInfo{Title: "Movie", Year: 2024},
	})
	if err != nil {
		t.Fatalf("export dvd: %v", err)
	}
	if result.IFOPath == "" || result.VOBPath == "" {
		t.Fatalf("expected IFO and VOB paths in result: %#v", result)
	}
	if !slices.Contains(analyzer.targets, ifoPath) {
		t.Fatalf("expected analyzer to scan ifo path %s, got %v", ifoPath, analyzer.targets)
	}
	if !slices.Contains(analyzer.targets, vobPath) {
		t.Fatalf("expected analyzer to scan vob path %s, got %v", vobPath, analyzer.targets)
	}
	if strings.TrimSpace(result.VOBText) == "" || strings.TrimSpace(result.VOBJSON) == "" {
		t.Fatalf("expected vob mediainfo outputs in result")
	}
	wantVOBCompleteName := "Complete name : " + filepath.Base(vobPath)
	if !strings.Contains(result.VOBText, wantVOBCompleteName) {
		t.Fatalf("expected VOB complete name %q, got %q", wantVOBCompleteName, result.VOBText)
	}
}

type fakeAnalyzer struct {
	text  string
	json  string
	calls int
}

func (f *fakeAnalyzer) Analyze(_ context.Context, _ string) (string, []byte, error) {
	f.calls++
	return f.text, []byte(f.json), nil
}

func fullSourceFileForTest(t *testing.T, path string) api.FullSourceFile {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return api.FullSourceFile{
		LocalPath: path,
		Size:      int64(len(payload)),
		SHA256:    hex.EncodeToString(sum[:]),
	}
}

func writeSourceEvidenceForTest(t *testing.T, dir string, evidence api.FullSourceFile) {
	t.Helper()
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MediaInfo.source.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

type panicAnalyzer struct {
	t *testing.T
}

func (p panicAnalyzer) Analyze(_ context.Context, _ string) (string, []byte, error) {
	p.t.Fatalf("runner should not be called")
	return "", nil, nil
}

type collisionAnalyzer struct {
	firstPath    string
	firstStarted chan struct{}
	secondDone   chan struct{}
}

func (a collisionAnalyzer) Analyze(_ context.Context, target string) (string, []byte, error) {
	if target == a.firstPath {
		close(a.firstStarted)
		<-a.secondDone
		return "General", []byte(`{"source":"first"}`), nil
	}
	return "General", []byte(`{"source":"second"}`), nil
}

type generationRaceAnalyzer struct {
	calls        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (a *generationRaceAnalyzer) Analyze(_ context.Context, _ string) (string, []byte, error) {
	if a.calls.Add(1) == 1 {
		close(a.firstStarted)
		<-a.releaseFirst
		return "General", []byte(`{"source":"first"}`), nil
	}
	return "General", []byte(`{"source":"second"}`), nil
}

type recordingAnalyzer struct {
	text    string
	json    string
	targets []string
}

func (r *recordingAnalyzer) Analyze(_ context.Context, target string) (string, []byte, error) {
	r.targets = append(r.targets, target)
	return r.text, []byte(r.json), nil
}
