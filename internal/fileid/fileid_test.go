package fileid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentity_DistinguishesCopiesButUnifiesMovesAndHardLinks(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "original.db")
	if err := os.WriteFile(original, []byte("database"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}
	originalID, err := Identity(original)
	if err != nil {
		t.Fatalf("identify original: %v", err)
	}

	hardLink := filepath.Join(dir, "hardlink.db")
	if err := os.Link(original, hardLink); err != nil {
		t.Fatalf("create hard link: %v", err)
	}
	hardLinkID, err := Identity(hardLink)
	if err != nil {
		t.Fatalf("identify hard link: %v", err)
	}
	if hardLinkID != originalID {
		t.Fatalf("hard link identity = %q, want %q", hardLinkID, originalID)
	}

	copyPath := filepath.Join(dir, "copy.db")
	if err := os.WriteFile(copyPath, []byte("database"), 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	copyID, err := Identity(copyPath)
	if err != nil {
		t.Fatalf("identify copy: %v", err)
	}
	if copyID == originalID {
		t.Fatalf("independent copy reused file identity %q", originalID)
	}

	moved := filepath.Join(dir, "moved.db")
	if err := os.Rename(original, moved); err != nil {
		t.Fatalf("move original: %v", err)
	}
	movedID, err := Identity(moved)
	if err != nil {
		t.Fatalf("identify moved file: %v", err)
	}
	if movedID != originalID {
		t.Fatalf("moved file identity = %q, want %q", movedID, originalID)
	}
}
