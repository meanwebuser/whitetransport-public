package carriers

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func TestGitRepositoryCarrierSeparateClonesAndVectorCursor(t *testing.T) {
	remote := initBareGitRemote(t)
	writer := newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "writer"), "writer-a")
	readerDir := filepath.Join(t.TempDir(), "reader")
	reader := newTestGitCarrier(t, remote, readerDir, "reader-a")
	endpoint := Endpoint{ID: "git.primary", Address: "session-control"}
	want := fabric.NewEnvelope("env-1", fabric.TrafficEgress, "test.payload", []byte("exact-payload"))
	if err := writer.Write(context.Background(), endpoint, want); err != nil {
		t.Fatalf("write envelope: %v", err)
	}
	first, err := reader.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if len(first.Envelopes) != 1 || first.Envelopes[0].ID != want.ID || string(first.Envelopes[0].Payload) != "exact-payload" {
		t.Fatalf("unexpected first read: %+v", first.Envelopes)
	}
	second, err := reader.Read(context.Background(), endpoint, first.Cursor)
	if err != nil || len(second.Envelopes) != 0 {
		t.Fatalf("same cursor replay err=%v envelopes=%+v", err, second.Envelopes)
	}
	restarted := newTestGitCarrier(t, remote, readerDir, "reader-a")
	afterRestart, err := restarted.Read(context.Background(), endpoint, first.Cursor)
	if err != nil || len(afterRestart.Envelopes) != 0 {
		t.Fatalf("restart replay err=%v envelopes=%+v", err, afterRestart.Envelopes)
	}
}

func TestGitRepositoryCarrierConcurrentWriterBranchesLoseNothing(t *testing.T) {
	remote := initBareGitRemote(t)
	endpoint := Endpoint{ID: "git.primary", Address: "egress"}
	writers := []*GitRepositoryCarrier{
		newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "writer-a"), "writer-a"),
		newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "writer-b"), "writer-b"),
	}
	var wait sync.WaitGroup
	for writerIndex, writer := range writers {
		for item := 0; item < 3; item++ {
			wait.Add(1)
			go func(writer *GitRepositoryCarrier, id string) {
				defer wait.Done()
				envelope := fabric.NewEnvelope(id, fabric.TrafficEgress, "test.concurrent", []byte(id))
				if err := writer.Write(context.Background(), endpoint, envelope); err != nil {
					t.Errorf("write %s: %v", id, err)
				}
			}(writer, fmt.Sprintf("writer-%d-item-%d", writerIndex, item))
		}
	}
	wait.Wait()
	reader := newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "reader"), "reader")
	result, err := reader.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatalf("read concurrent writers: %v", err)
	}
	ids := make([]string, 0, len(result.Envelopes))
	for _, envelope := range result.Envelopes {
		ids = append(ids, envelope.ID)
	}
	sort.Strings(ids)
	if len(ids) != 6 {
		t.Fatalf("received %d envelopes, want 6: %v", len(ids), ids)
	}
	for index := 1; index < len(ids); index++ {
		if ids[index] == ids[index-1] {
			t.Fatalf("duplicate envelope %s", ids[index])
		}
	}
}

func TestGitRepositoryCarrierRejectsCredentialBearingRemoteURL(t *testing.T) {
	_, err := NewGitRepositoryCarrier(GitRepositoryConfig{
		RemoteURL: "https://user:secret@example.invalid/transport.git",
		WorkDir:   filepath.Join(t.TempDir(), "clone"),
		WriterID:  "writer-a",
	})
	if err == nil {
		t.Fatal("credential-bearing remote URL was accepted")
	}
}

func TestGitRepositoryCarrierRejectsLocalRemoteOutsideFixture(t *testing.T) {
	remote := initBareGitRemote(t)
	for _, remoteURL := range []string{remote, "file://" + remote} {
		t.Run(remoteURL, func(t *testing.T) {
			_, err := NewGitRepositoryCarrier(GitRepositoryConfig{
				RemoteURL: remoteURL,
				WorkDir:   filepath.Join(t.TempDir(), "clone"),
				WriterID:  "writer",
			})
			if err == nil || !strings.Contains(err.Error(), "local fixture") {
				t.Fatalf("local remote %q error = %v, want production rejection", remoteURL, err)
			}
		})
	}
}

func TestGitRepositoryCarrierRejectsDisappearedConsumedWriterBranch(t *testing.T) {
	remote := initBareGitRemote(t)
	writer := newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "writer"), "writer-a")
	reader := newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "reader"), "reader-a")
	endpoint := Endpoint{ID: "git.primary", Address: "integrity"}
	if err := writer.Write(context.Background(), endpoint, fabric.NewEnvelope("env", fabric.TrafficEgress, "test", []byte("payload"))); err != nil {
		t.Fatal(err)
	}
	first, err := reader.Read(context.Background(), endpoint, "")
	if err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, "--git-dir="+remote, "update-ref", "-d", "refs/heads/"+writer.writerBranch(endpoint))
	if _, err := reader.Read(context.Background(), endpoint, first.Cursor); err == nil {
		t.Fatal("deleted previously-consumed writer branch was accepted")
	}
}

func TestGitRepositoryCarrierConstructedDescriptorIsPolicyEligible(t *testing.T) {
	remote := initBareGitRemote(t)
	carrier := newTestGitCarrier(t, remote, filepath.Join(t.TempDir(), "policy-eligible"), "policy-writer")
	descriptor := carrier.Descriptor()
	if !HasCapability(descriptor, CapBulk) {
		t.Fatalf("constructed Git descriptor capabilities = %v, want %s", descriptor.Capabilities, CapBulk)
	}
	derived := DeriveTrafficClasses(descriptor.Capabilities)
	for _, traffic := range []fabric.TrafficClass{fabric.TrafficBulk, fabric.TrafficEgress} {
		found := false
		for _, candidate := range derived {
			if candidate == traffic {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("constructed Git descriptor derived traffic = %v, want %s", derived, traffic)
		}
	}
}

func newTestGitCarrier(t *testing.T, remote, workDir, writerID string) *GitRepositoryCarrier {
	t.Helper()
	carrier, err := NewGitRepositoryCarrier(GitRepositoryConfig{
		RemoteURL:         remote,
		WorkDir:           workDir,
		WriterID:          writerID,
		allowLocalFixture: true,
	})
	if err != nil {
		t.Fatalf("new Git carrier %s: %v", writerID, err)
	}
	return carrier
}

func initBareGitRemote(t *testing.T) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "transport.git")
	command := exec.Command("git", "init", "--bare", "--initial-branch=unused", remote)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v: %s", err, output)
	}
	return remote
}

func runGitTestCommand(t *testing.T, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	} else {
		return string(output)
	}
	return ""
}
