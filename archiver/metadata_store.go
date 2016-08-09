package archiver

import (
	"fmt"
	"github.com/gtfierro/durandal/common"
	"github.com/pkg/errors"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"net"
)

type mongoConfig struct {
	address *net.TCPAddr
}

type mongoStore struct {
	session  *mgo.Session
	db       *mgo.Database
	metadata *mgo.Collection
}

func newMongoStore(c *mongoConfig) *mongoStore {
	var err error
	m := &mongoStore{}
	log.Noticef("Connecting to MongoDB at %v...", c.address.String())
	m.session, err = mgo.Dial(c.address.String())
	if err != nil {
		log.Criticalf("Could not connect to MongoDB: %v", err)
		return nil
	}
	log.Notice("...connected!")
	// fetch/create collections and db reference
	m.db = m.session.DB("durandal")
	m.metadata = m.db.C("metadata")

	// add indexes. This will fail Fatal
	m.addIndexes()

	return m
}

func (m *mongoStore) addIndexes() {
	var err error
	// create indexes
	index := mgo.Index{
		Key:        []string{"UUID"},
		Unique:     false,
		DropDups:   false,
		Background: false,
		Sparse:     false,
	}
	err = m.metadata.EnsureIndex(index)
	if err != nil {
		log.Fatalf("Could not create index on metadata.UUID (%v)", err)
	}

	index.Key = []string{"Path"}
	index.Unique = false
	err = m.metadata.EnsureIndex(index)
	if err != nil {
		log.Fatalf("Could not create index on metadata.Path (%v)", err)
	}

	index.Key = []string{"SrcURI"}
	index.Unique = false
	err = m.metadata.EnsureIndex(index)
	if err != nil {
		log.Fatalf("Could not create index on metadata.URI (%v)", err)
	}

	index.Key = []string{"Key"}
	index.Unique = false
	err = m.metadata.EnsureIndex(index)
	if err != nil {
		log.Fatalf("Could not create index on metadata.Key (%v)", err)
	}

}

func (m *mongoStore) GetUnitOfTime(VK string, uuid common.UUID) (common.UnitOfTime, error) {
	var (
		c   int
		err error
		res interface{}
	)
	uot := common.UOT_S
	query := m.metadata.Find(bson.M{"uuid": uuid}).Select(bson.M{"UnitofTime": 1})
	if c, err = query.Count(); err != nil {
		return uot, errors.Wrapf(err, "Could not find any UnitofTime records")
	} else if c == 0 {
		return uot, fmt.Errorf("no stream named %v", uuid)
	}
	err = query.One(&res)
	if entry, found := res.(bson.M)["UnitofTime"]; found {
		if uotInt, isInt := entry.(int); isInt {
			uot = common.UnitOfTime(uotInt)
		} else {
			return uot, fmt.Errorf("Invalid UnitOfTime retrieved? %v", entry)
		}
		uot = common.UnitOfTime(entry.(int))
		if uot == 0 {
			uot = common.UOT_S
		}
	}
	return uot, nil
}

/*
Here we describe the mechanism for how to retrieve metadata using a given VK.
First, we run the unaltered query and retrieve the set of resulting docs. Then, we must
filter the results by:
- remove if the VK cannot build a chain to the URI of a returned stream
- if the VK cannot build a chain to the URI for a piece of metadata:
  - if the key is in tags, remove the result
  - if the key is in "where", remove the result

This requires testing and finish implementing the DOT stuff
*/
func (m *mongoStore) GetMetadata(VK string, tags []string, where common.Dict) (*common.MetadataGroup, error) {
	var (
		whereClause bson.M
		_results    []bson.M
	)
	if len(where) != 0 {
		whereClause = where.ToBSON()
	}
	staged := m.metadata.Find(whereClause)
	selectTags := bson.M{"_id": 0}
	if len(tags) != 0 {
		for _, tag := range tags {
			selectTags[tag] = 1
		}
	}
	if err := staged.Select(selectTags).All(&_results); err != nil {
		return nil, errors.Wrap(err, "Could not select tags")
	}
	for _, doc := range _results {
		log.Debug(doc)
	}
	return nil, nil
}

func (m *mongoStore) GetDistinct(VK string, tag string, where common.Dict) (*common.MetadataGroup, error) {
	//var (
	//	whereClause bson.M
	//	distincts   []string
	//)
	//if len(where) != 0 {
	//	whereClause = where.ToBSON()
	//}
	//err := m.metadata.Find(whereClause).Distinct(tag, &distincts)
	return nil, nil
}

func (m *mongoStore) SaveMetadata(records []*common.MetadataRecord) error {
	if len(records) == 0 {
		log.Infof("Aborting metadata insert with 0 records")
		return nil
	}
	for _, rec := range records {
		log.Debugf("Inserting %+v", rec)
		if _, err := m.metadata.Upsert(bson.M{"Key": rec.Key, "SrcURI": rec.SrcURI}, rec); err != nil {
			return err
		}
	}
	return nil
}

//func (m *mongoStore) FlushMetadataGroup(grp *common.MetadataGroup) error {
//	grp.Lock()
//	defer grp.Unlock()
//	if len(grp.Records) == 0 {
//		log.Infof("Aborting metadata insert with 0 records")
//		return nil
//	}
//	for _, rec := range grp.Records {
//		log.Debugf("Inserting %+v", rec)
//		if _, err := m.metadata.Upsert(bson.M{"Key": rec.Key, "SrcURI": rec.SrcURI}, rec); err != nil {
//			return err
//		}
//	}
//	for k, _ := range grp.Records {
//		delete(grp.Records, k)
//	}
//
//	return nil
//}

func (m *mongoStore) RemoveMetadata(VK string, tags []string, where common.Dict) error {
	return nil
}
