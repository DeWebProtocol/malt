package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedArchiveRoundTrip(t *testing.T) {
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "project")
	if err := os.MkdirAll(filepath.Join(source, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "docs", "readme.txt"), []byte("private contents\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(filepath.Join(source, "docs", "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{1, 2, 3, 4}
	archive := filepath.Join(t.TempDir(), "snapshot")
	info, err := CreateArchive(context.Background(), source, archive, 7, key)
	if err != nil {
		t.Fatal(err)
	}
	if info.Epoch != 7 || info.Bytes <= archiveHeaderSize {
		t.Fatalf("archive info = %#v", info)
	}
	ciphertext, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("private contents")) || bytes.Contains(ciphertext, []byte("readme.txt")) {
		t.Fatal("encrypted archive exposes plaintext content or path")
	}

	destination := t.TempDir()
	if err := restoreArchive(context.Background(), archive, destination, func(epoch uint32) ([32]byte, error) {
		if epoch != 7 {
			t.Fatalf("restore requested epoch %d", epoch)
		}
		return key, nil
	}, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "project", "docs", "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "private contents\n" {
		t.Fatalf("restored body = %q", got)
	}
	restoredInfo, err := os.Stat(filepath.Join(destination, "project", "docs", "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if restoredInfo.Mode().Perm() != sourceInfo.Mode().Perm() {
		t.Fatalf("restored mode = %#o, want %#o", restoredInfo.Mode().Perm(), sourceInfo.Mode().Perm())
	}
}

func TestEncryptedArchiveRejectsWrongKeyBeforeExtraction(t *testing.T) {
	source := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(source, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 1, [32]byte{1}); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	err := restoreArchive(context.Background(), archive, destination, func(uint32) ([32]byte, error) {
		return [32]byte{2}, nil
	}, false)
	if err == nil {
		t.Fatal("wrong key restored an archive")
	}
	entries, readErr := os.ReadDir(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("wrong-key restore created entries: %v", entries)
	}
}

func TestSafeArchiveTargetRejectsTraversalAndBackslashes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"../escape", "/absolute", `dir\escape`} {
		if _, err := safeArchiveTarget(root, name); err == nil {
			t.Fatalf("safeArchiveTarget accepted %q", name)
		}
	}
}

func TestCreateArchiveRejectsSymlinkRestoreCannotAccept(t *testing.T) {
	source := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(source, "outside")
	if err := os.Symlink(filepath.Join(t.TempDir(), "secret"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 1, [32]byte{1}); err == nil {
		t.Fatal("archive accepted an absolute symlink that restore must reject")
	}
}

func TestCreateArchiveRejectsTargetInsideSource(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "staging", "snapshot")
	if _, err := CreateArchive(context.Background(), source, target, 1, [32]byte{1}); err == nil {
		t.Fatal("archive accepted a target inside its source")
	}
}

func TestCreateArchiveRejectsSymlinkedTargetInsideSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	actualStaging := filepath.Join(source, "staging")
	if err := os.MkdirAll(actualStaging, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "staging-link")
	if err := os.Symlink(actualStaging, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := CreateArchive(context.Background(), source, filepath.Join(link, "snapshot"), 1, [32]byte{1}); err == nil {
		t.Fatal("archive accepted a symlinked target inside its source")
	}
}

func TestRestoreOverwriteDoesNotFollowExistingSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(source, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 1, key); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	link := filepath.Join(destination, "secret.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := restoreArchive(context.Background(), archive, destination, func(uint32) ([32]byte, error) {
		return key, nil
	}, true); err != nil {
		t.Fatal(err)
	}
	gotOutside, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutside) != "outside" {
		t.Fatalf("restore followed symlink and changed outside file to %q", gotOutside)
	}
	gotRestored, err := os.ReadFile(link)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotRestored) != "restored" {
		t.Fatalf("restored body = %q", gotRestored)
	}
}

func TestRestoreRejectsSymlinkDestinationRoot(t *testing.T) {
	source := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(source, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 1, key); err != nil {
		t.Fatal(err)
	}
	actual := t.TempDir()
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Symlink(actual, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := restoreArchive(context.Background(), archive, destination, func(uint32) ([32]byte, error) {
		return key, nil
	}, false); err == nil {
		t.Fatal("restore accepted a symlink destination root")
	}
	entries, err := os.ReadDir(actual)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("symlink destination received restored entries: %v", entries)
	}
}

func TestRestoreCanonicalizesSymlinkDestinationAncestor(t *testing.T) {
	source := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(source, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 1, key); err != nil {
		t.Fatal(err)
	}
	actualParent := t.TempDir()
	parentLink := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(actualParent, parentLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	destination := filepath.Join(parentLink, "restore")
	if err := restoreArchive(context.Background(), archive, destination, func(uint32) ([32]byte, error) {
		return key, nil
	}, false); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(actualParent, "restore", "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "restored" {
		t.Fatalf("restored body through canonical ancestor = %q", got)
	}
}
