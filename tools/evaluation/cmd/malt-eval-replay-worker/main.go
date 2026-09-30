// Command malt-eval-replay-worker exposes actual incremental UnixFS writes.
// Its protocol is deliberately separate from both legacy RQ3 and the next
// protocol's latency executor. It makes no crash-durability claim.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"reflect"

	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
)

const requestSchema = "malt-replay-worker-request/v1"
const responseSchema = "malt-replay-worker-response/v1"

type request struct {
	Schema    string               `json:"schema_version"`
	ID        string               `json:"request_id"`
	Operation string               `json:"operation"`
	Profile   string               `json:"profile"`
	Run       *rq3baseline.RunSpec `json:"run,omitempty"`
}

type response struct {
	Schema           string                     `json:"schema_version"`
	ID               string                     `json:"request_id"`
	OK               bool                       `json:"ok"`
	Error            string                     `json:"error,omitempty"`
	Profiles         []string                   `json:"profiles,omitempty"`
	Records          []rq3baseline.CommitRecord `json:"records,omitempty"`
	ObjectRoles      [][]string                 `json:"object_roles,omitempty"`
	Complete         bool                       `json:"complete"`
	ReadbackVerified bool                       `json:"readback_verified"`
	CommitListSHA256 string                     `json:"commit_list_sha256,omitempty"`
	Applied          uint32                     `json:"applied"`
	RetainedObjects  int64                      `json:"retained_objects,string"`
	RetainedBytes    int64                      `json:"retained_bytes,string"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "replay worker accepts no arguments")
		os.Exit(1)
	}
	if err := run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, input io.Reader, output io.Writer) (returnErr error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), rq3baseline.MaximumJSONLRecordBytes)
	encoder := json.NewEncoder(output)
	var session *rq3baseline.StreamSession
	defer func() {
		if session != nil {
			returnErr = errors.Join(returnErr, session.Close())
		}
	}()
	var profile string
	var layout rq3baseline.LayoutSpec
	var applied uint32
	capability, finished := false, false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var req request
		if !json.Valid(scanner.Bytes()) {
			return fmt.Errorf("request must contain one JSON value")
		}
		if err := strictjson.Decode(scanner.Bytes(), &req); err != nil {
			return err
		}
		if req.Schema != requestSchema || req.ID == "" || finished {
			return fmt.Errorf("invalid replay request envelope or terminal stream")
		}
		res := response{Schema: responseSchema, ID: req.ID, OK: true}
		var err error
		switch req.Operation {
		case "capabilities":
			if capability || session != nil || req.Run != nil || req.Profile != "" {
				return fmt.Errorf("capabilities must be first")
			}
			capability = true
			res.Profiles = []string{"merkledag-nested", "hamt-nested", "hamt-flat"}
		case "start":
			if !capability || session != nil || req.Run == nil {
				return fmt.Errorf("invalid stream start")
			}
			profile, layout = req.Profile, req.Run.Layout
			var record rq3baseline.CommitRecord
			switch profile {
			case "merkledag-nested":
				if req.Run.System != rq3baseline.SystemMerkleDAGUnixFS {
					return fmt.Errorf("Merkle DAG profile mismatch")
				}
				session, record, err = rq3baseline.StartReplayStream(ctx, *req.Run, false)
			case "hamt-nested":
				if req.Run.System != rq3baseline.SystemHAMTUnixFS {
					return fmt.Errorf("HAMT profile mismatch")
				}
				session, record, err = rq3baseline.StartReplayStream(ctx, *req.Run, false)
			case "hamt-flat":
				session, record, err = rq3baseline.StartReplayStream(ctx, *req.Run, true)
			default:
				return fmt.Errorf("unsupported profile %q", profile)
			}
			if err == nil {
				paths := make([]string, len(req.Run.Snapshot.Files))
				for i, file := range req.Run.Snapshot.Files {
					paths[i] = file.Path
				}
				err = session.VerifyPaths(ctx, paths)
			}
			if err == nil {
				res.Records = []rq3baseline.CommitRecord{record}
				applied = 1
			}
		case "chunk":
			if session == nil || req.Run == nil || req.Profile != profile || !reflect.DeepEqual(layout, req.Run.Layout) || req.Run.Snapshot.CommitID != "" || req.Run.Snapshot.Files != nil {
				return fmt.Errorf("chunk does not bind active stream")
			}
			if req.Run.System != rq3baseline.SystemMerkleDAGUnixFS && req.Run.System != rq3baseline.SystemHAMTUnixFS {
				return fmt.Errorf("chunk system mismatch")
			}
			if (profile == "merkledag-nested") != (req.Run.System == rq3baseline.SystemMerkleDAGUnixFS) {
				return fmt.Errorf("chunk system mismatch")
			}
			res.Records, err = session.ApplyChunkWithReadback(ctx, req.Run.Commits)
			if err == nil {
				applied += uint32(len(res.Records))
			}
		case "finish":
			if session == nil || req.Run != nil || req.Profile != profile {
				return fmt.Errorf("invalid stream finish")
			}
			res.CommitListSHA256, err = session.CommitListSHA256()
			if err == nil {
				err = session.VerifyAll(ctx)
			}
			if err == nil {
				res.RetainedObjects, res.RetainedBytes, err = session.RetainedCAS()
			}
			if err == nil {
				err = session.Close()
			}
			if err == nil {
				session = nil
				finished = true
				res.Complete = true
			}
		default:
			return fmt.Errorf("unsupported replay operation %q", req.Operation)
		}
		res.Applied = applied
		for _, record := range res.Records {
			roles := make([]string, len(record.CAS.Events))
			for i, event := range record.CAS.Events {
				roles[i] = event.ObjectRole
			}
			res.ObjectRoles = append(res.ObjectRoles, roles)
		}
		res.ReadbackVerified = applied > 0 && err == nil
		if err != nil {
			res.OK = false
			res.Error = err.Error()
		}
		if encodeErr := encoder.Encode(res); encodeErr != nil {
			return encodeErr
		}
		if err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !finished {
		return fmt.Errorf("incomplete replay stream")
	}
	return nil
}
