// Package store provides a durable, embedded storage layer backed by BoltDB
// (bbolt) for Interaction records and Payload objects.
//
// All methods are safe for concurrent use; bbolt handles its own locking at
// the bucket level.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/google/uuid"
	"github.com/newuser-admin/claude/interact/pkg/events"
)

var (
	bucketInteractions = []byte("interactions")
	bucketPayloads     = []byte("payloads")
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps a bbolt database and exposes typed CRUD operations.
type Store struct {
	db *bolt.DB
}

// Open opens (or creates) the bbolt database at path.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open db %q: %w", path, err)
	}

	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketInteractions, bucketPayloads} {
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
	return &Store{db: db}, nil
}

// Close releases the database file handle.
func (s *Store) Close() error { return s.db.Close() }

// ─────────────────────────────────────────────────────────────────────────────
// Interactions
// ─────────────────────────────────────────────────────────────────────────────

// SaveInteraction creates or replaces an Interaction.
// If ia.ID is empty a new UUID is assigned.
// UpdatedAt and Version are always refreshed.
func (s *Store) SaveInteraction(ia *events.Interaction) error {
	if ia.ID == "" {
		ia.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if ia.CreatedAt.IsZero() {
		ia.CreatedAt = now
	}
	ia.UpdatedAt = now
	ia.Version++

	data, err := json.Marshal(ia)
	if err != nil {
		return err
	}

	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketInteractions).Put([]byte(ia.ID), data)
	})
}

// GetInteraction returns the Interaction with the given ID.
func (s *Store) GetInteraction(id string) (*events.Interaction, error) {
	var ia events.Interaction
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketInteractions).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &ia)
	})
	if err != nil {
		return nil, err
	}
	return &ia, nil
}

// ListInteractions returns all stored Interactions ordered by UpdatedAt desc.
func (s *Store) ListInteractions() ([]*events.Interaction, error) {
	var list []*events.Interaction
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketInteractions)
		return b.ForEach(func(_, v []byte) error {
			var ia events.Interaction
			if err := json.Unmarshal(v, &ia); err != nil {
				return err
			}
			list = append(list, &ia)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	// Sort newest first
	sortInteractions(list)
	return list, nil
}

// DeleteInteraction removes an Interaction by ID.
func (s *Store) DeleteInteraction(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketInteractions)
		if b.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return b.Delete([]byte(id))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Payloads
// ─────────────────────────────────────────────────────────────────────────────

// SavePayload creates or replaces a Payload.
// If p.ID is empty a new UUID is assigned.
// The SHA-256 checksum is computed from p.Data automatically.
func (s *Store) SavePayload(p *events.Payload) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	sum := sha256.Sum256(p.Data)
	p.Checksum = hex.EncodeToString(sum[:])

	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPayloads).Put([]byte(p.ID), data)
	})
}

// GetPayload returns the Payload with the given ID.
func (s *Store) GetPayload(id string) (*events.Payload, error) {
	var p events.Payload
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bucketPayloads).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return json.Unmarshal(data, &p)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPayloads returns all Payloads.  If pendingOnly is true only un-acked
// payloads are returned.
func (s *Store) ListPayloads(pendingOnly bool) ([]*events.Payload, error) {
	var list []*events.Payload
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketPayloads).ForEach(func(_, v []byte) error {
			var p events.Payload
			if err := json.Unmarshal(v, &p); err != nil {
				return err
			}
			if pendingOnly && p.Acked {
				return nil
			}
			list = append(list, &p)
			return nil
		})
	})
	return list, err
}

// AckPayload marks a Payload as acknowledged by the given client.
func (s *Store) AckPayload(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketPayloads)
		raw := b.Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		var p events.Payload
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		p.Acked = true
		data, err := json.Marshal(&p)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), data)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func sortInteractions(list []*events.Interaction) {
	// Insertion sort — list is usually small and nearly sorted.
	for i := 1; i < len(list); i++ {
		j := i
		for j > 0 && list[j-1].UpdatedAt.Before(list[j].UpdatedAt) {
			list[j-1], list[j] = list[j], list[j-1]
			j--
		}
	}
}
