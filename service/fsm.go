package service

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/ioutil"
	"os"

	"github.com/hashicorp/raft"
	"github.com/icexin/raftkv/config"
	"github.com/icexin/raftkv/proto"
	"github.com/syndtr/goleveldb/leveldb"
)

var (
	errBadMethod = errors.New("bad method")
)

type FSM struct {
	cfg *config.DB
	*leveldb.DB
}

func NewFSM(cfg *config.DB) (*FSM, error) {
	// Create a temporary path for the state store
	tmpPath, err := ioutil.TempDir(cfg.Dir, "state")
	if err != nil {
		return nil, err
	}

	db, err := leveldb.OpenFile(tmpPath, nil)
	if err != nil {
		return nil, err
	}

	return &FSM{
		cfg: cfg,
		DB:  db,
	}, nil
}

func (f *FSM) Apply(l *raft.Log) interface{} {
	return f.write(l.Data)
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	snapshot, err := f.GetSnapshot()
	if err != nil {
		return nil, err
	}
	return &fsmSnapshot{snapshot}, nil
}

func (f *FSM) Restore(r io.ReadCloser) error {
	defer r.Close()

	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	// Create a temporary path for the state store
	tmpPath, err := ioutil.TempDir(f.cfg.Dir, "state")
	if err != nil {
		return err
	}

	tr := tar.NewReader(zr)
	err = Untar(tmpPath, tr)
	if err != nil {
		return err
	}

	db, err := leveldb.OpenFile(tmpPath, nil)
	if err != nil {
		return err
	}

	f.DB = db
	return nil
}

func (f *FSM) write(cmd []byte) error {
	req := new(proto.Request)
	err := proto.Unmarshal(cmd, req)
	if err != nil {
		return err
	}
	if req.Action != proto.Write {
		return errBadMethod
	}
	return f.Put(req.Key, req.Data, nil)
}

// fsmSnapshot implement FSMSnapshot interface
type fsmSnapshot struct {
	snapshot *leveldb.Snapshot
}

// At first, walk all kvs, write temp leveldb.
// Second, make tar.gz for temp leveldb dir
func (f *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	// Create a temporary path for the state store
	tmpPath, err := ioutil.TempDir(os.TempDir(), "state")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpPath)

	db, err := leveldb.OpenFile(tmpPath, nil)
	if err != nil {
		return err
	}
	iter := f.snapshot.NewIterator(nil, nil)
	for iter.Next() {
		err = db.Put(iter.Key(), iter.Value(), nil)
		if err != nil {
			db.Close()
			sink.Cancel()
			return err
		}
	}
	iter.Release()
	db.Close()

	// make tar.gz
	w := gzip.NewWriter(sink)
	err = Tar(tmpPath, w)
	if err != nil {
		sink.Cancel()
		return err
	}

	err = w.Close()
	if err != nil {
		sink.Cancel()
		return err
	}

	sink.Close()
	return nil
}

func (f *fsmSnapshot) Release() {
	f.snapshot.Release()
}