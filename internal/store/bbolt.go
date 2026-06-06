package store

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketBoards     = []byte("boards")
	bucketCards      = []byte("cards")
	bucketDeliveries = []byte("deliveries")
	bucketLocks      = []byte("locks")
)

// Bbolt is a bbolt-backed Store.
type Bbolt struct {
	db  *bolt.DB
	now func() time.Time
}

// OpenBbolt opens (creating if needed) a bbolt store at path.
func OpenBbolt(path string) (*Bbolt, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt %s: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketBoards, bucketCards, bucketDeliveries, bucketLocks} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init buckets: %w", err)
	}
	return &Bbolt{db: db, now: time.Now}, nil
}

func (s *Bbolt) GetBoard(projectID string) (BoardRecord, bool, error) {
	var rec BoardRecord
	ok, err := s.get(bucketBoards, projectID, &rec)
	return rec, ok, err
}

func (s *Bbolt) PutBoard(projectID string, rec BoardRecord) error {
	return s.put(bucketBoards, projectID, rec)
}

func (s *Bbolt) GetCard(issueNodeID string) (CardRecord, bool, error) {
	var rec CardRecord
	ok, err := s.get(bucketCards, issueNodeID, &rec)
	return rec, ok, err
}

func (s *Bbolt) PutCard(issueNodeID string, rec CardRecord) error {
	return s.put(bucketCards, issueNodeID, rec)
}

func (s *Bbolt) Close() error { return s.db.Close() }

func (s *Bbolt) SeenDelivery(id string) (bool, error) {
	var seen bool
	err := s.db.View(func(tx *bolt.Tx) error {
		seen = tx.Bucket(bucketDeliveries).Get([]byte(id)) != nil
		return nil
	})
	return seen, err
}

func (s *Bbolt) MarkDelivery(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDeliveries).Put([]byte(id), []byte{1})
	})
}

// lockValue is the JSON shape stored in the locks bucket.
type lockValue struct {
	Owner     string `json:"owner"`
	ExpiresAt int64  `json:"expires_at"` // unix nanoseconds
}

func (s *Bbolt) AcquireLock(cardID, owner string, ttl time.Duration) (bool, error) {
	now := s.now()
	acquired := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLocks)
		if data := b.Get([]byte(cardID)); data != nil {
			var lv lockValue
			if err := json.Unmarshal(data, &lv); err != nil {
				return err
			}
			if lv.Owner != owner && now.UnixNano() < lv.ExpiresAt {
				return nil // held by someone else, not expired
			}
		}
		out, err := json.Marshal(lockValue{Owner: owner, ExpiresAt: now.Add(ttl).UnixNano()})
		if err != nil {
			return err
		}
		acquired = true
		return b.Put([]byte(cardID), out)
	})
	return acquired, err
}

func (s *Bbolt) ReleaseLock(cardID, owner string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketLocks)
		data := b.Get([]byte(cardID))
		if data == nil {
			return nil
		}
		var lv lockValue
		if err := json.Unmarshal(data, &lv); err != nil {
			return err
		}
		if lv.Owner == owner {
			return b.Delete([]byte(cardID))
		}
		return nil
	})
}

func (s *Bbolt) put(bucket []byte, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put([]byte(key), data)
	})
}

func (s *Bbolt) get(bucket []byte, key string, v any) (bool, error) {
	var found bool
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucket).Get([]byte(key))
		if data == nil {
			return nil
		}
		found = true
		return json.Unmarshal(data, v)
	})
	return found, err
}
