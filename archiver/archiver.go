package archiver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	bw2 "github.com/immesys/bw2bind"
	"github.com/op/go-logging"
	"github.com/pkg/errors"
	"github.com/pkg/profile"

	"github.com/gtfierro/pundat/common"
	"github.com/gtfierro/pundat/dots"
	"github.com/gtfierro/pundat/querylang"
	"github.com/gtfierro/pundat/scraper"
)

// logger
var log *logging.Logger

// set up logging facilities
func init() {
	log = logging.MustGetLogger("archiver")
	var format = "%{color}%{level} %{shortfile} %{time:Jan 02 15:04:05} %{color:reset} ▶ %{message}"
	var logBackend = logging.NewLogBackend(os.Stderr, "", 0)
	logBackendLeveled := logging.AddModuleLevel(logBackend)
	logging.SetBackend(logBackendLeveled)
	logging.SetFormatter(logging.MustStringFormatter(format))
}

type Archiver struct {
	bw        *bw2.BW2Client
	vk        string
	MD        MetadataStore
	dotmaster *dots.DotMaster
	TS        TimeseriesStore
	svc       *bw2.Service
	iface     *bw2.Interface
	vm        *viewManager
	qp        *querylang.QueryProcessor
	config    *Config
	stop      chan bool

	bw2address string
	bw2entity  string
}

func NewArchiver(c *Config) (a *Archiver) {
	scraper.Init()
	a = &Archiver{
		config:     c,
		stop:       make(chan bool),
		bw2address: c.BOSSWAVE.Address,
		bw2entity:  c.BOSSWAVE.Entityfile,
	}
	// enable profiling if configured
	if c.Benchmark.EnableCPUProfile {
		defer profile.Start(profile.CPUProfile, profile.ProfilePath(".")).Stop()
	} else if c.Benchmark.EnableMEMProfile {
		defer profile.Start(profile.MemProfile, profile.ProfilePath(".")).Stop()
	} else if c.Benchmark.EnableBlockProfile {
		defer profile.Start(profile.BlockProfile, profile.ProfilePath(".")).Stop()
	}

	go func() {
		log.Fatal(http.ListenAndServe("localhost:6064", nil))
	}()
	go func() {
		for _ = range time.Tick(10 * time.Second) {
			_active_streams := atomic.LoadInt64(&currentStreams)
			_completed := atomic.SwapInt64(&completedWrites, 0)
			_pending := atomic.LoadInt64(&currentWrites)
			log.Infof("active=%d completed=%d pending=%d", _active_streams, _completed, _pending)
		}
	}()

	// setup metadata
	mongoaddr, err := net.ResolveTCPAddr("tcp4", c.Metadata.Address)
	if err != nil {
		log.Fatal(errors.Wrapf(err, "Could not resolve Metadata address %s", c.Metadata.Address))
	}
	a.MD = newMongoStore(&mongoConfig{address: mongoaddr, collectionPrefix: c.Metadata.CollectionPrefix})

	a.TS = newBTrDBv4(&btrdbv4Config{addresses: []string{c.BtrDB.Address}})
	if a.TS == nil {
		log.Fatal("could not connect to btrdb")
	}
	//	a.TS = NewCSVDB()

	// setup bosswave
	a.bw = bw2.ConnectOrExit(c.BOSSWAVE.Address)
	a.bw.OverrideAutoChainTo(true)
	a.vk = a.bw.SetEntityFileOrExit(c.BOSSWAVE.Entityfile)

	// setup dot master
	// parse duration
	expiry, err := time.ParseDuration(c.Archiver.BlockExpiry)
	if err != nil {
		log.Fatal(errors.Wrapf(err, "Could not parse expiry duration %s", c.Archiver.BlockExpiry))
	}
	a.dotmaster = dots.NewDotMaster(a.bw, expiry)

	// setup view manager
	a.vm = newViewManager(a.bw, a.vk, c.BOSSWAVE, a.MD, a.TS, a.bw2address, a.bw2entity)

	a.qp = querylang.NewQueryProcessor()

	queryClient := bw2.ConnectOrExit(c.BOSSWAVE.Address)
	queryClient.OverrideAutoChainTo(true)
	queryClient.SetEntityFileOrExit(c.BOSSWAVE.Entityfile)
	a.svc = queryClient.RegisterService(c.BOSSWAVE.DeployNS, "s.giles")
	a.iface = a.svc.RegisterInterface("_", "i.archiver")
	queryChan, err := queryClient.Subscribe(&bw2.SubscribeParams{
		URI: a.iface.SlotURI("query"),
	})
	if err != nil {
		log.Error(errors.Wrap(err, "Could not subscribe"))
	}
	log.Noticef("Listening on %s", a.iface.SlotURI("query"))
	common.NewWorkerPool(queryChan, a.listenQueries, 1000).Start()

	return a
}

func (a *Archiver) Serve() {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigs
		log.Info("GOT SIGNAL-->", sig)
		a.stop <- true
	}()
	for _, namespace := range a.config.BOSSWAVE.ListenNS {
		go a.vm.subscribeNamespace(ctx, namespace)
		time.Sleep(2 * time.Second)
	}

	<-a.stop

	cancel()
	a.TS.Disconnect()
}

func (a *Archiver) Stop() {
	a.stop <- true
}

func (a *Archiver) listenQueries(msg *bw2.SimpleMessage) {
	var (
		// the publisher of the message. We incorporate this into the signal URI
		fromVK string
		// the computed signal based on the VK and query nonce
		signalURI string
		// query message
		query KeyValueQuery
	)
	start := time.Now()
	fromVK = msg.From
	po := msg.GetOnePODF(bw2.PODFGilesKeyValueQuery)
	if po == nil { // no query found
		return
	}

	if obj, ok := po.(bw2.MsgPackPayloadObject); !ok {
		log.Error("Received query was not msgpack")
	} else if err := obj.ValueInto(&query); err != nil {
		log.Error(errors.Wrap(err, "Could not unmarshal received query"))
		return
	}

	signalURI = fmt.Sprintf("%s,queries", fromVK[:len(fromVK)-1])

	log.Infof("Got query %+v", query)
	mdRes, tsRes, statsRes, changedRes, err := a.HandleQuery(fromVK, query.Query)
	if err != nil {
		msg := QueryError{
			Query: query.Query,
			Nonce: query.Nonce,
			Error: err.Error(),
		}
		po, _ := bw2.CreateMsgPackPayloadObject(bw2.PONumGilesQueryError, msg)
		log.Error(errors.Wrap(err, "Error evaluating query"))
		if err := a.iface.PublishSignal(signalURI, po); err != nil {
			log.Error(errors.Wrap(err, "Error sending response"))
		}
		return
	}

	// assemble replies
	var reply []bw2.PayloadObject

	if len(mdRes) > 0 {
		metadataPayload := POsFromMetadataGroup(query.Nonce, mdRes)
		reply = append(reply, metadataPayload)
	}

	if len(tsRes)+len(statsRes) > 0 {
		timeseriesPayload := POsFromTimeseriesGroup(query.Nonce, tsRes, statsRes)
		reply = append(reply, timeseriesPayload)
	}

	if len(changedRes) > 0 {
		changedPayload := POsFromChangedGroup(query.Nonce, changedRes)
		reply = append(reply, changedPayload)
	}

	// if we do not have any results, send back an empty metadata payload
	if len(reply) == 0 {
		metadataPayload := POsFromMetadataGroup(query.Nonce, mdRes)
		reply = append(reply, metadataPayload)
	}

	log.Infof("Reply to %s: %d POs MD/TS/Stat/Chng (%d/%d/%d/%d) (took %s)", fromVK, len(reply), len(mdRes), len(tsRes), len(statsRes), len(changedRes), time.Since(start))

	if err := a.iface.PublishSignal(signalURI, reply...); err != nil {
		log.Error(errors.Wrap(err, "Error sending response"))
	}
}

func (a *Archiver) HandleQuery(vk, query string) (mdResult []common.MetadataGroup, tsResult []common.Timeseries, statsResult []common.StatisticTimeseries, changedResult []common.ChangedRange, err error) {
	parsed := a.qp.Parse(query)
	if parsed.Err != nil {
		err = fmt.Errorf("Error (%v) in query \"%v\" (error at %v)\n", parsed.Err, query, parsed.ErrPos)
		return
	}

	switch parsed.QueryType {
	case querylang.SELECT_TYPE:
		if parsed.Distinct {
			var results []string
			params := parsed.GetParams().(*common.DistinctParams)
			results, err = a.DistinctTag(vk, params)
			// sandwidth the results into a metadata record
			record := &common.MetadataRecord{
				Key:   params.Tag,
				Value: results,
			}
			mdResult = []common.MetadataGroup{
				{Records: map[string]*common.MetadataRecord{params.Tag: record}},
			}
			return
		}
		params := parsed.GetParams().(*common.TagParams)
		mdResult, err = a.SelectTags(vk, params)
		return
	case querylang.DATA_TYPE:
		params := parsed.GetParams().(*common.DataParams)
		if params.IsStatistical || params.IsWindow {
			statsResult, err = a.SelectStatisticalData(vk, params)
			return
		}
		if params.IsChangedRanges {
			changedResult, err = a.GetChangedRanges(params)
			return
		}
		switch parsed.Data.Dtype {
		case querylang.IN_TYPE:
			tsResult, err = a.SelectDataRange(vk, params)
			return
		case querylang.BEFORE_TYPE:
			tsResult, err = a.SelectDataBefore(vk, params)
			return
		case querylang.AFTER_TYPE:
			tsResult, err = a.SelectDataAfter(vk, params)
			return
		}
	}

	return
}
