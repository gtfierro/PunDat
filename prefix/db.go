package prefix

import (
	"bytes"
	"encoding/binary"
	"github.com/boltdb/bolt"
	"github.com/gtfierro/durandal/common"
	"github.com/pkg/errors"
	"log"
)

// Handles the association of UUIDs to MD URIs
/*
A "stream" is uniquely identified by a:
	- specified URI
	- PO number
	- value expression
We give a UUID to each stream as a unique pointer.

An Archive Request is a contract to generate streams given some set of parameters:
	- a URI pattern to subscribe to for timeseries data
	- a method for retrieving metadata:
		- can inherit metadata from inheriting from prefixes on a specified URI
		- can retrieve metadata from URI patterns
	- a method for retrieving a value for each message published

An Archive Request can generate several streams.

We have one process that subscribes to the wildcard timeseries URIs; this process
"discovers" the specific timeseries URIs.

We have another process that subscribes to the wildcard metadata URIs; this process
"discovers" the specific metadata URIs.


We need to track the specific timeseries and specific metadata URIs.

Given a specific metadata URI, I want to find which timeseries URIs it is a prefix of


What's the API we want to support?

// ignore if already exists
AddMetadataURI(uri string) error
AddTimeseriesURI(uri string) error

// given a prefix, returns the set of metadata URIs it is a prefix of
GetMetadataSuperstrings(prefix string) ([]string, error)

// given a prefix, returns the set of timeseries URIs it is a prefix of
GetTimeseriesSuperstrings(prefix string) ([]string, error)

// given a timeseries URI, return the set of uuids matching it
GetUUIDsFromURI(uri string) ([]common.UUID, error)

*/

var tsBucket = []byte("timeseries")
var mdBucket = []byte("metadata")
var uuidBucket = []byte("uuid")

type PrefixStore struct {
	db *bolt.DB
}

func NewPrefixStore(filename string) *PrefixStore {
	db, err := bolt.Open(filename, 0600, nil)
	if err != nil {
		log.Fatal(errors.Wrap(err, "Could not open database file"))
	}
	store := &PrefixStore{
		db: db,
	}
	db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(tsBucket)
		if err != nil {
			return errors.Wrap(err, "Could not create timeseries bucket")
		}
		_, err = tx.CreateBucketIfNotExists(mdBucket)
		if err != nil {
			return errors.Wrap(err, "Could not create metadata bucket")
		}
		_, err = tx.CreateBucketIfNotExists(uuidBucket)
		if err != nil {
			return errors.Wrap(err, "Could not create uuid bucket")
		}
		return nil
	})
	return store
}

//TODO: maybe add a "read" check first to optimistically skip writing every time?
func (store *PrefixStore) AddMetadataURI(uri string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(mdBucket)
		id, _ := b.NextSequence()
		return b.Put(itob(id), []byte(uri))
	})
}

func (store *PrefixStore) AddTimeseriesURI(uri string) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(tsBucket)
		id, _ := b.NextSequence()
		return b.Put([]byte(uri), itob(id))
	})
}

func (store *PrefixStore) AddUUIDURIMapping(uri string, uuid common.UUID) error {
	return store.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(uuidBucket)
		ub, err := b.CreateBucketIfNotExists([]byte(uri))
		if err != nil {
			return err
		}
		id, _ := ub.NextSequence()
		bytes := uuid.Bytes()
		//log.Println("PUT URI BUCKET", uri)
		return ub.Put(itob(id), bytes[:])
	})
}

// assumes prefix has already been cleaned
func (store *PrefixStore) GetMetadataSuperstrings(prefix string) ([]string, error) {
	var matching []string
	err := store.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(mdBucket).Cursor()
		bpfx := []byte(prefix)
		for k, _ := c.Seek(bpfx); bytes.HasPrefix(k, bpfx); k, _ = c.Next() {
			matching = append(matching, string(k))
		}
		return nil
	})
	return matching, err
}

// assumes prefix has already been cleaned
func (store *PrefixStore) GetTimeseriesSuperstrings(prefix string) ([]string, error) {
	var matching []string
	err := store.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(tsBucket).Cursor()
		bpfx := []byte(prefix)
		for k, _ := c.Seek(bpfx); bytes.HasPrefix(k, bpfx); k, _ = c.Next() {
			matching = append(matching, string(k))
		}
		return nil
	})
	return matching, err
}

func (store *PrefixStore) GetUUIDsFromURI(uri string) (uuids []common.UUID, err error) {
	superURIs, err := store.GetTimeseriesSuperstrings(uri)
	if err != nil {
		return
	}
	foundUUIDs := make(map[[16]byte]bool)
	err = store.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(uuidBucket)
		for _, suri := range superURIs {
			ub := b.Bucket([]byte(suri))
			if ub == nil {
				return nil
			}
			c := ub.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				foundUUIDs[common.UUID(v).Bytes()] = true
			}
		}
		return nil
	})
	for uuid, _ := range foundUUIDs {
		uuids = append(uuids, common.UUIDFromBytes(uuid))
	}
	return uuids, err
}

func itob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}