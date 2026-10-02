package rq3baseline

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	merkledag "github.com/ipfs/boxo/ipld/merkledag"
	unixfs "github.com/ipfs/boxo/ipld/unixfs"
	cid "github.com/ipfs/go-cid"
)

func testArchive(t *testing.T, segmentBytes int64) *archiveCAS {
	t.Helper()
	s, err := newArchiveCAS(t.TempDir(), segmentBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.closeHandles(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func archiveKey(t *testing.T, data []byte, codec uint64) cid.Cid {
	t.Helper()
	key, err := clientcas.CIDForBlock(clientcas.Block{Data: data, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestReplayArchiveMatchesFileStoreAcrossAllProfiles(t *testing.T) {
	for _, profile := range []string{"merkledag-nested", "hamt-nested", "hamt-flat"} {
		t.Run(profile, func(t *testing.T) {
			system, directory, fanout := SystemHAMTUnixFS, "hamt", 256
			if profile == "merkledag-nested" {
				system, directory, fanout = SystemMerkleDAGUnixFS, "basic", 0
			}
			spec := baseSpec(system, directory, fanout, []FrozenFile{frozenFile("a/b", []byte("initial")), frozenFile("stable", []byte("unchanged"))})
			packed := func() *accountingStore {
				s := newAccountingStore()
				if s.initErr == nil {
					s.archive, s.initErr = newArchiveCAS(s.root, 16<<10)
				}
				return s
			}
			var sessions []*StreamSession
			var previous *CommitRecord
			var expectedObjects, expectedBytes int64
			compare := func(a, b CommitRecord) {
				t.Helper()
				a.ClientPhases, b.ClientPhases = ClientPhases{}, ClientPhases{}
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("logical record differs at %s", a.CommitID)
				}
			}
			for _, factory := range []func() *accountingStore{newAccountingStore, packed} {
				s, initial, err := startStreamWithStore(t.Context(), spec, profile == "hamt-flat", true, factory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.Close(); err != nil {
						t.Error(err)
					}
				})
				sessions = append(sessions, s)
				if previous != nil {
					compare(*previous, initial)
				} else {
					previous = &initial
				}
			}
			expectedObjects += int64(previous.CAS.Total.NewlyPersistedObjects)
			expectedBytes += previous.CAS.Total.NewlyPersistedBytes
			body := []byte("initial")
			for i := 0; i < 80; i++ {
				next := []byte(fmt.Sprintf("version-%d-%s", i%7, bytes.Repeat([]byte("x"), i%11)))
				if i%9 == 0 {
					next = body
				} // no-op and cross-commit reuse
				commit := Commit{CommitID: fmt.Sprintf("archive-update-%d", i), Mutations: []Mutation{replaceMutation("a/b", body, next)}}
				var records []CommitRecord
				for _, session := range sessions {
					got, err := session.ApplyChunkWithReadback(t.Context(), []Commit{commit})
					if err != nil {
						t.Fatal(err)
					}
					records = append(records, got[0])
					if err := session.VerifyAll(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				compare(records[0], records[1])
				expectedObjects += int64(records[0].CAS.Total.NewlyPersistedObjects)
				expectedBytes += records[0].CAS.Total.NewlyPersistedBytes
				body = next
			}
			for _, session := range sessions {
				objects, size, err := session.RetainedCAS()
				if err != nil || objects != expectedObjects || size != expectedBytes {
					t.Fatalf("inventory = %d/%d, want %d/%d: %v", objects, size, expectedObjects, expectedBytes, err)
				}
			}
			report, err := sessions[1].FinishReplayStorage()
			if err != nil {
				t.Fatal(err)
			}
			if !report.Reconciled || report.CARWriteBytes != report.CARBytes || report.CARBytes-report.FramingBytes != expectedBytes || report.IndexFiles == 0 || report.IndexWriterBytes == 0 || report.IndexWriterComplete {
				t.Fatalf("invalid backend report: %+v", report)
			}
			if _, err := sessions[1].store.Get(t.Context(), archiveKey(t, body, cid.Raw)); err == nil {
				t.Fatal("sealed store accepted reads")
			}
		})
	}
}

func TestReplayArchiveUsesWholeCIDAndPreservesDedup(t *testing.T) {
	s := newReplayAccountingStore()
	t.Cleanup(func() {
		if err := s.close(); err != nil {
			t.Error(err)
		}
	})
	data, err := merkledag.NodeWithData(unixfs.FolderPBData()).EncodeProtobuf(false)
	if err != nil {
		t.Fatal(err)
	}
	s.beginPhase()
	for _, codec := range []uint64{cid.Raw, cid.DagProtobuf, cid.Raw, cid.DagProtobuf} {
		key, err := s.PutWithCodec(t.Context(), data, codec)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(t.Context(), key)
		if err != nil || !bytes.Equal(data, got) {
			t.Fatalf("whole CID lookup failed: %v", err)
		}
	}
	first, _, _ := s.finishPhase()
	if first.Total.NewlyPersistedObjects != 2 || first.Total.DuplicateObjects != 2 {
		t.Fatalf("dedup = %+v", first.Total)
	}
	s.beginPhase()
	if _, err := s.PutWithCodec(t.Context(), data, cid.Raw); err != nil {
		t.Fatal(err)
	}
	second, _, _ := s.finishPhase()
	if second.Total.AlreadyPresentObjects != 1 || second.Total.NewlyPersistedObjects != 0 {
		t.Fatal("cross-phase dedup changed")
	}
	report, err := s.archive.inventory()
	if err != nil || report.Objects != 2 {
		t.Fatalf("whole CID inventory: %+v, %v", report, err)
	}
}

func TestReplayArchiveManyObjectsUseFewFiles(t *testing.T) {
	s := testArchive(t, 64<<10)
	const count = 20000
	var total int64
	for i := 0; i < count; i++ {
		data := binary.LittleEndian.AppendUint64(nil, uint64(i))
		if err := s.appendNew(archiveKey(t, data, cid.Raw), data); err != nil {
			t.Fatal(err)
		}
		total += int64(len(data))
	}
	// Reverse-order lookups span many sealed shards and exercise eviction.
	for i := count - 1; i >= 0; i -= 101 {
		data := binary.LittleEndian.AppendUint64(nil, uint64(i))
		got, err := s.get(archiveKey(t, data, cid.Raw))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("read %d: %v", i, err)
		}
		if s.readers.Len() > archiveReadHandles {
			t.Fatal("unbounded reader handles")
		}
	}
	report, err := s.finish()
	if err != nil {
		t.Fatal(err)
	}
	if report.Objects != count || report.BodyBytes != total || report.CARFiles < 2 || report.Entries >= 100 {
		t.Fatalf("inode reduction failed: %+v", report)
	}
	t.Logf("%d objects, %d CAR segments, %d CAS filesystem entries, %d body bytes, %d CAR bytes, %d index bytes", count, report.CARFiles, report.Entries, report.BodyBytes, report.CARBytes, report.IndexBytes)
}

func TestReplayArchiveRejectsCorruptOrIncompleteStorage(t *testing.T) {
	for _, scenario := range []string{"body", "index-offset", "missing-index", "extra-index", "truncated-frame", "truncated-varint", "duplicate-frame", "missing-segment"} {
		t.Run(scenario, func(t *testing.T) {
			s := testArchive(t, 4096)
			data := []byte("checked payload")
			key := archiveKey(t, data, cid.Raw)
			if err := s.appendNew(key, data); err != nil {
				t.Fatal(err)
			}
			location, err := s.index.Get(key.Bytes(), nil)
			if err != nil {
				t.Fatal(err)
			}
			p, err := decodeArchiveLocation(location)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "body":
				_, err = s.active.WriteAt([]byte("!"), int64(p.offset+p.length-1))
			case "index-offset":
				p.offset++
				err = s.index.Put(key.Bytes(), p.encode(), nil)
			case "missing-index":
				err = s.index.Delete(key.Bytes(), nil)
			case "extra-index":
				err = s.index.Put(archiveKey(t, []byte("phantom"), cid.Raw).Bytes(), location, nil)
			case "truncated-frame":
				err = s.active.Truncate(s.activeBytes - 1)
			case "truncated-varint":
				err = s.write([]byte{0x80})
			case "duplicate-frame":
				err = s.appendNew(key, data)
			case "missing-segment":
				f := s.active
				s.active = nil
				err = f.Close()
				if err == nil {
					err = os.Remove(s.path(1))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "body" || scenario == "index-offset" || scenario == "missing-segment" || scenario == "truncated-frame" {
				if _, err := s.get(key); err == nil || os.IsNotExist(err) {
					t.Fatalf("corruption treated as valid/missing block: %v", err)
				}
			}
			if _, err := s.inventory(); err == nil {
				t.Fatal("damaged CAR/index accepted")
			}
		})
	}
}

func TestReplayArchiveFailurePoisonsAppend(t *testing.T) {
	s := testArchive(t, 4096)
	data := []byte("first")
	if err := s.appendNew(archiveKey(t, data, cid.Raw), data); err != nil {
		t.Fatal(err)
	}
	// Read-only replacement deterministically injects a failed filesystem write.
	if err := s.active.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s.active, err = os.Open(s.path(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"second", "third"} {
		body := []byte(text)
		if err := s.appendNew(archiveKey(t, body, cid.Raw), body); err == nil {
			t.Fatal("failed archive continued accepting writes")
		}
	}
	if _, err := s.inventory(); err == nil {
		t.Fatal("failed run became a complete inventory")
	}
	missing := archiveKey(t, []byte("absent"), cid.Raw)
	if _, err := s.get(missing); errors.Is(err, os.ErrNotExist) {
		t.Fatal("poisoned store treated as normal absence")
	}
}

func TestReplayArchiveIndexFailureRetainsUnpublishedFrame(t *testing.T) {
	s := testArchive(t, 4096)
	data := []byte("first")
	if err := s.appendNew(archiveKey(t, data, cid.Raw), data); err != nil {
		t.Fatal(err)
	}
	before := s.carWritten
	if err := s.index.SetReadOnly(); err != nil {
		t.Fatal(err)
	}
	data = []byte("index publication fails after CAR append")
	if err := s.appendNew(archiveKey(t, data, cid.Raw), data); err == nil {
		t.Fatal("read-only index accepted append")
	}
	if s.carWritten <= before {
		t.Fatal("partial execution's actual CAR write was erased")
	}
	if _, err := s.inventory(); err == nil {
		t.Fatal("unpublished frame accepted as completed replay")
	}
}

func TestReplayArchiveCreationRejectsExistingIndex(t *testing.T) {
	directory := t.TempDir()
	s, err := newArchiveCAS(directory, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.closeHandles(); err != nil {
		t.Fatal(err)
	}
	if _, err := newArchiveCAS(directory, 4096); err == nil {
		t.Fatal("existing run reused")
	}
	if _, err := os.Stat(filepath.Join(directory, "index")); err != nil {
		t.Fatal(err)
	}
}
