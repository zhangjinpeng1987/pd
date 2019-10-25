// Copyright 2016 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/coreos/go-semver/semver"
	"github.com/pingcap/log"
	"github.com/pingcap/pd/pkg/metricutil"
	"github.com/pingcap/pd/pkg/typeutil"
	"github.com/pingcap/pd/server/core"
	"github.com/pingcap/pd/server/namespace"
	"github.com/pingcap/pd/server/schedule"
	"github.com/pkg/errors"
	"go.etcd.io/etcd/embed"
	"go.etcd.io/etcd/pkg/transport"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config is the pd server configuration.
type Config struct {
	*flag.FlagSet `json:"-"`

	Version bool `json:"-"`

	ConfigCheck bool `json:"-"`

	ClientUrls          string `toml:"client-urls" json:"client-urls"`
	PeerUrls            string `toml:"peer-urls" json:"peer-urls"`
	AdvertiseClientUrls string `toml:"advertise-client-urls" json:"advertise-client-urls"`
	AdvertisePeerUrls   string `toml:"advertise-peer-urls" json:"advertise-peer-urls"`

	Name              string `toml:"name" json:"name"`
	DataDir           string `toml:"data-dir" json:"data-dir"`
	ForceNewCluster   bool   `json:"force-new-cluster"`
	EnableGRPCGateway bool   `json:"enable-grpc-gateway"`

	InitialCluster      string `toml:"initial-cluster" json:"initial-cluster"`
	InitialClusterState string `toml:"initial-cluster-state" json:"initial-cluster-state"`

	// Join to an existing pd cluster, a string of endpoints.
	Join string `toml:"join" json:"join"`

	// LeaderLease time, if leader doesn't update its TTL
	// in etcd after lease time, etcd will expire the leader key
	// and other servers can campaign the leader again.
	// Etcd only supports seconds TTL, so here is second too.
	LeaderLease int64 `toml:"lease" json:"lease"`

	// Log related config.
	Log log.Config `toml:"log" json:"log"`

	// Backward compatibility.
	LogFileDeprecated  string `toml:"log-file" json:"log-file"`
	LogLevelDeprecated string `toml:"log-level" json:"log-level"`

	// TsoSaveInterval is the interval to save timestamp.
	TsoSaveInterval typeutil.Duration `toml:"tso-save-interval" json:"tso-save-interval"`

	Metric metricutil.MetricConfig `toml:"metric" json:"metric"`

	Schedule ScheduleConfig `toml:"schedule" json:"schedule"`

	Replication ReplicationConfig `toml:"replication" json:"replication"`

	Namespace map[string]NamespaceConfig `json:"namespace"`

	PDServerCfg PDServerConfig `toml:"pd-server" json:"pd-server"`

	ClusterVersion semver.Version `json:"cluster-version"`

	// QuotaBackendBytes Raise alarms when backend size exceeds the given quota. 0 means use the default quota.
	// the default size is 2GB, the maximum is 8GB.
	QuotaBackendBytes typeutil.ByteSize `toml:"quota-backend-bytes" json:"quota-backend-bytes"`
	// AutoCompactionMode is either 'periodic' or 'revision'. The default value is 'periodic'.
	AutoCompactionMode string `toml:"auto-compaction-mode" json:"auto-compaction-mode"`
	// AutoCompactionRetention is either duration string with time unit
	// (e.g. '5m' for 5-minute), or revision unit (e.g. '5000').
	// If no time unit is provided and compaction mode is 'periodic',
	// the unit defaults to hour. For example, '5' translates into 5-hour.
	// The default retention is 1 hour.
	// Before etcd v3.3.x, the type of retention is int. We add 'v2' suffix to make it backward compatible.
	AutoCompactionRetention string `toml:"auto-compaction-retention" json:"auto-compaction-retention-v2"`

	// TickInterval is the interval for etcd Raft tick.
	TickInterval typeutil.Duration `toml:"tick-interval"`
	// ElectionInterval is the interval for etcd Raft election.
	ElectionInterval typeutil.Duration `toml:"election-interval"`
	// Prevote is true to enable Raft Pre-Vote.
	// If enabled, Raft runs an additional election phase
	// to check whether it would get enough votes to win
	// an election, thus minimizing disruptions.
	PreVote bool `toml:"enable-prevote"`

	Security SecurityConfig `toml:"security" json:"security"`

	LabelProperty LabelPropertyConfig `toml:"label-property" json:"label-property"`

	configFile string

	// For all warnings during parsing.
	WarningMsgs []string

	// NamespaceClassifier is for classifying stores/regions into different
	// namespaces.
	NamespaceClassifier string `toml:"namespace-classifier" json:"namespace-classifier"`

	// Only test can change them.
	nextRetryDelay             time.Duration
	DisableStrictReconfigCheck bool

	HeartbeatStreamBindInterval typeutil.Duration

	LeaderPriorityCheckInterval typeutil.Duration

	logger   *zap.Logger
	logProps *log.ZapProperties
}

// NewConfig creates a new config.
func NewConfig() *Config {
	cfg := &Config{}
	cfg.FlagSet = flag.NewFlagSet("pd", flag.ContinueOnError)
	fs := cfg.FlagSet

	fs.BoolVar(&cfg.Version, "V", false, "print version information and exit")
	fs.BoolVar(&cfg.Version, "version", false, "print version information and exit")
	fs.StringVar(&cfg.configFile, "config", "", "Config file")
	fs.BoolVar(&cfg.ConfigCheck, "config-check", false, "check config file validity and exit")

	fs.StringVar(&cfg.Name, "name", "", "human-readable name for this pd member")

	fs.StringVar(&cfg.DataDir, "data-dir", "", "path to the data directory (default 'default.${name}')")
	fs.StringVar(&cfg.ClientUrls, "client-urls", defaultClientUrls, "url for client traffic")
	fs.StringVar(&cfg.AdvertiseClientUrls, "advertise-client-urls", "", "advertise url for client traffic (default '${client-urls}')")
	fs.StringVar(&cfg.PeerUrls, "peer-urls", defaultPeerUrls, "url for peer traffic")
	fs.StringVar(&cfg.AdvertisePeerUrls, "advertise-peer-urls", "", "advertise url for peer traffic (default '${peer-urls}')")
	fs.StringVar(&cfg.InitialCluster, "initial-cluster", "", "initial cluster configuration for bootstrapping, e,g. pd=http://127.0.0.1:2380")
	fs.StringVar(&cfg.Join, "join", "", "join to an existing cluster (usage: cluster's '${advertise-client-urls}'")

	fs.StringVar(&cfg.Metric.PushAddress, "metrics-addr", "", "prometheus pushgateway address, leaves it empty will disable prometheus push.")

	fs.StringVar(&cfg.Log.Level, "L", "", "log level: debug, info, warn, error, fatal (default 'info')")
	fs.StringVar(&cfg.Log.File.Filename, "log-file", "", "log file path")
	fs.BoolVar(&cfg.Log.File.LogRotate, "log-rotate", true, "rotate log")
	fs.StringVar(&cfg.NamespaceClassifier, "namespace-classifier", "table", "namespace classifier (default 'table')")

	fs.StringVar(&cfg.Security.CAPath, "cacert", "", "Path of file that contains list of trusted TLS CAs")
	fs.StringVar(&cfg.Security.CertPath, "cert", "", "Path of file that contains X509 certificate in PEM format")
	fs.StringVar(&cfg.Security.KeyPath, "key", "", "Path of file that contains X509 key in PEM format")
	fs.BoolVar(&cfg.ForceNewCluster, "force-new-cluster", false, "Force to create a new one-member cluster")

	cfg.Namespace = make(map[string]NamespaceConfig)

	return cfg
}

const (
	defaultLeaderLease             = int64(3)
	defaultNextRetryDelay          = time.Second
	defaultCompactionMode          = "periodic"
	defaultAutoCompactionRetention = "1h"

	defaultName                = "pd"
	defaultClientUrls          = "http://127.0.0.1:2379"
	defaultPeerUrls            = "http://127.0.0.1:2380"
	defaultInitialClusterState = embed.ClusterStateFlagNew

	// etcd use 100ms for heartbeat and 1s for election timeout.
	// We can enlarge both a little to reduce the network aggression.
	// now embed etcd use TickMs for heartbeat, we will update
	// after embed etcd decouples tick and heartbeat.
	defaultTickInterval = 500 * time.Millisecond
	// embed etcd has a check that `5 * tick > election`
	defaultElectionInterval = 3000 * time.Millisecond

	defaultMetricsPushInterval = 15 * time.Second

	defaultHeartbeatStreamRebindInterval = time.Minute

	defaultLeaderPriorityCheckInterval = time.Minute

	defaultUseRegionStorage    = true
	defaultMaxResetTsGap       = 24 * time.Hour
	defaultStrictlyMatchLabel  = false
	defaultEnableGRPCGateway   = true
	defaultDisableErrorVerbose = true
)

func adjustString(v *string, defValue string) {
	if len(*v) == 0 {
		*v = defValue
	}
}

func adjustUint64(v *uint64, defValue uint64) {
	if *v == 0 {
		*v = defValue
	}
}

func adjustInt64(v *int64, defValue int64) {
	if *v == 0 {
		*v = defValue
	}
}

func adjustFloat64(v *float64, defValue float64) {
	if *v == 0 {
		*v = defValue
	}
}

func adjustDuration(v *typeutil.Duration, defValue time.Duration) {
	if v.Duration == 0 {
		v.Duration = defValue
	}
}

func adjustSchedulers(v *SchedulerConfigs, defValue SchedulerConfigs) {
	if len(*v) == 0 {
		*v = defValue
	}
}

// Parse parses flag definitions from the argument list.
func (c *Config) Parse(arguments []string) error {
	// Parse first to get config file.
	err := c.FlagSet.Parse(arguments)
	if err != nil {
		return errors.WithStack(err)
	}

	// Load config file if specified.
	var meta *toml.MetaData
	if c.configFile != "" {
		meta, err = c.configFromFile(c.configFile)
		if err != nil {
			return err
		}

		// Backward compatibility for toml config
		if c.LogFileDeprecated != "" && c.Log.File.Filename == "" {
			c.Log.File.Filename = c.LogFileDeprecated
			msg := fmt.Sprintf("log-file in %s is deprecated, use [log.file] instead", c.configFile)
			c.WarningMsgs = append(c.WarningMsgs, msg)
		}
		if c.LogLevelDeprecated != "" && c.Log.Level == "" {
			c.Log.Level = c.LogLevelDeprecated
			msg := fmt.Sprintf("log-level in %s is deprecated, use [log] instead", c.configFile)
			c.WarningMsgs = append(c.WarningMsgs, msg)
		}
		if meta.IsDefined("schedule", "disable-raft-learner") {
			msg := fmt.Sprintf("disable-raft-learner in %s is deprecated", c.configFile)
			c.WarningMsgs = append(c.WarningMsgs, msg)
		}
	}

	// Parse again to replace with command line options.
	err = c.FlagSet.Parse(arguments)
	if err != nil {
		return errors.WithStack(err)
	}

	if len(c.FlagSet.Args()) != 0 {
		return errors.Errorf("'%s' is an invalid flag", c.FlagSet.Arg(0))
	}

	err = c.Adjust(meta)
	return err
}

// Validate is used to validate if some configurations are right.
func (c *Config) Validate() error {
	if c.Join != "" && c.InitialCluster != "" {
		return errors.New("-initial-cluster and -join can not be provided at the same time")
	}
	dataDir, err := filepath.Abs(c.DataDir)
	if err != nil {
		return errors.WithStack(err)
	}
	logFile, err := filepath.Abs(c.Log.File.Filename)
	if err != nil {
		return errors.WithStack(err)
	}
	rel, err := filepath.Rel(dataDir, filepath.Dir(logFile))
	if err != nil {
		return errors.WithStack(err)
	}
	if !strings.HasPrefix(rel, "..") {
		return errors.New("log directory shouldn't be the subdirectory of data directory")
	}

	return nil
}

// Utility to test if a configuration is defined.
type configMetaData struct {
	meta *toml.MetaData
	path []string
}

func newConfigMetadata(meta *toml.MetaData) *configMetaData {
	return &configMetaData{meta: meta}
}

func (m *configMetaData) IsDefined(key string) bool {
	if m.meta == nil {
		return false
	}
	keys := append([]string(nil), m.path...)
	keys = append(keys, key)
	return m.meta.IsDefined(keys...)
}

func (m *configMetaData) Child(path ...string) *configMetaData {
	newPath := append([]string(nil), m.path...)
	newPath = append(newPath, path...)
	return &configMetaData{
		meta: m.meta,
		path: newPath,
	}
}

func (m *configMetaData) CheckUndecoded() error {
	if m.meta == nil {
		return nil
	}
	undecoded := m.meta.Undecoded()
	if len(undecoded) == 0 {
		return nil
	}
	errInfo := "Config contains undefined item: "
	for _, key := range undecoded {
		errInfo += key.String() + ", "
	}
	return errors.New(errInfo[:len(errInfo)-2])
}

// Adjust is used to adjust the PD configurations.
func (c *Config) Adjust(meta *toml.MetaData) error {
	configMetaData := newConfigMetadata(meta)
	if err := configMetaData.CheckUndecoded(); err != nil {
		c.WarningMsgs = append(c.WarningMsgs, err.Error())
	}

	if c.Name == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return err
		}
		adjustString(&c.Name, fmt.Sprintf("%s-%s", defaultName, hostname))
	}
	adjustString(&c.DataDir, fmt.Sprintf("default.%s", c.Name))

	if err := c.Validate(); err != nil {
		return err
	}

	adjustString(&c.ClientUrls, defaultClientUrls)
	adjustString(&c.AdvertiseClientUrls, c.ClientUrls)
	adjustString(&c.PeerUrls, defaultPeerUrls)
	adjustString(&c.AdvertisePeerUrls, c.PeerUrls)
	adjustDuration(&c.Metric.PushInterval, defaultMetricsPushInterval)

	if len(c.InitialCluster) == 0 {
		// The advertise peer urls may be http://127.0.0.1:2380,http://127.0.0.1:2381
		// so the initial cluster is pd=http://127.0.0.1:2380,pd=http://127.0.0.1:2381
		items := strings.Split(c.AdvertisePeerUrls, ",")

		sep := ""
		for _, item := range items {
			c.InitialCluster += fmt.Sprintf("%s%s=%s", sep, c.Name, item)
			sep = ","
		}
	}

	adjustString(&c.InitialClusterState, defaultInitialClusterState)

	if len(c.Join) > 0 {
		if _, err := url.Parse(c.Join); err != nil {
			return errors.Errorf("failed to parse join addr:%s, err:%v", c.Join, err)
		}
	}

	adjustInt64(&c.LeaderLease, defaultLeaderLease)

	adjustDuration(&c.TsoSaveInterval, time.Duration(defaultLeaderLease)*time.Second)

	if c.nextRetryDelay == 0 {
		c.nextRetryDelay = defaultNextRetryDelay
	}

	adjustString(&c.AutoCompactionMode, defaultCompactionMode)
	adjustString(&c.AutoCompactionRetention, defaultAutoCompactionRetention)
	adjustDuration(&c.TickInterval, defaultTickInterval)
	adjustDuration(&c.ElectionInterval, defaultElectionInterval)

	adjustString(&c.NamespaceClassifier, "table")

	adjustString(&c.Metric.PushJob, c.Name)

	if err := c.Schedule.adjust(configMetaData.Child("schedule")); err != nil {
		return err
	}
	if err := c.Replication.adjust(configMetaData.Child("replication")); err != nil {
		return err
	}

	if err := c.PDServerCfg.adjust(configMetaData.Child("pd-server")); err != nil {
		return err
	}

	c.adjustLog(configMetaData.Child("log"))
	adjustDuration(&c.HeartbeatStreamBindInterval, defaultHeartbeatStreamRebindInterval)

	adjustDuration(&c.LeaderPriorityCheckInterval, defaultLeaderPriorityCheckInterval)

	if !configMetaData.IsDefined("enable-prevote") {
		c.PreVote = true
	}
	if !configMetaData.IsDefined("enable-grpc-gateway") {
		c.EnableGRPCGateway = defaultEnableGRPCGateway
	}
	return nil
}

func (c *Config) adjustLog(meta *configMetaData) {
	if !meta.IsDefined("disable-error-verbose") {
		c.Log.DisableErrorVerbose = defaultDisableErrorVerbose
	}
}

// Clone returns a cloned configuration.
func (c *Config) Clone() *Config {
	cfg := &Config{}
	*cfg = *c
	return cfg
}

func (c *Config) String() string {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "<nil>"
	}
	return string(data)
}

// configFromFile loads config from file.
func (c *Config) configFromFile(path string) (*toml.MetaData, error) {
	meta, err := toml.DecodeFile(path, c)
	return &meta, errors.WithStack(err)
}

// ScheduleConfig is the schedule configuration.
type ScheduleConfig struct {
	// If the snapshot count of one store is greater than this value,
	// it will never be used as a source or target store.
	MaxSnapshotCount    uint64 `toml:"max-snapshot-count,omitempty" json:"max-snapshot-count"`
	MaxPendingPeerCount uint64 `toml:"max-pending-peer-count,omitempty" json:"max-pending-peer-count"`
	// If both the size of region is smaller than MaxMergeRegionSize
	// and the number of rows in region is smaller than MaxMergeRegionKeys,
	// it will try to merge with adjacent regions.
	MaxMergeRegionSize uint64 `toml:"max-merge-region-size,omitempty" json:"max-merge-region-size"`
	MaxMergeRegionKeys uint64 `toml:"max-merge-region-keys,omitempty" json:"max-merge-region-keys"`
	// SplitMergeInterval is the minimum interval time to permit merge after split.
	SplitMergeInterval typeutil.Duration `toml:"split-merge-interval,omitempty" json:"split-merge-interval"`
	// EnableOneWayMerge is the option to enable one way merge. This means a Region can only be merged into the next region of it.
	EnableOneWayMerge bool `toml:"enable-one-way-merge,omitempty" json:"enable-one-way-merge,string"`
	// PatrolRegionInterval is the interval for scanning region during patrol.
	PatrolRegionInterval typeutil.Duration `toml:"patrol-region-interval,omitempty" json:"patrol-region-interval"`
	// MaxStoreDownTime is the max duration after which
	// a store will be considered to be down if it hasn't reported heartbeats.
	MaxStoreDownTime typeutil.Duration `toml:"max-store-down-time,omitempty" json:"max-store-down-time"`
	// MaxColdDataTime is
	MaxColdDataTime typeutil.Duration `toml:"max-cold-data-time,omitempty" json:"max-cold-data-time"`
	// LeaderScheduleLimit is the max coexist leader schedules.
	LeaderScheduleLimit uint64 `toml:"leader-schedule-limit,omitempty" json:"leader-schedule-limit"`
	// LeaderScheduleStrategy is the option to balance leader, there are some strategics supported: ["count", "size"], default: "count"
	LeaderScheduleStrategy string `toml:"leader-schedule-strategy,omitempty" json:"leader-schedule-strategy,string"`
	// RegionScheduleLimit is the max coexist region schedules.
	RegionScheduleLimit uint64 `toml:"region-schedule-limit,omitempty" json:"region-schedule-limit"`
	// ReplicaScheduleLimit is the max coexist replica schedules.
	ReplicaScheduleLimit uint64 `toml:"replica-schedule-limit,omitempty" json:"replica-schedule-limit"`
	// MergeScheduleLimit is the max coexist merge schedules.
	MergeScheduleLimit uint64 `toml:"merge-schedule-limit,omitempty" json:"merge-schedule-limit"`
	// HotRegionScheduleLimit is the max coexist hot region schedules.
	HotRegionScheduleLimit uint64 `toml:"hot-region-schedule-limit,omitempty" json:"hot-region-schedule-limit"`
	// HotRegionCacheHitThreshold is the cache hits threshold of the hot region.
	// If the number of times a region hits the hot cache is greater than this
	// threshold, it is considered a hot region.
	HotRegionCacheHitsThreshold uint64 `toml:"hot-region-cache-hits-threshold,omitempty" json:"hot-region-cache-hits-threshold"`
	// StoreBalanceRate is the maximum of balance rate for each store.
	StoreBalanceRate float64 `toml:"store-balance-rate,omitempty" json:"store-balance-rate"`
	// TolerantSizeRatio is the ratio of buffer size for balance scheduler.
	TolerantSizeRatio float64 `toml:"tolerant-size-ratio,omitempty" json:"tolerant-size-ratio"`
	//
	//      high space stage         transition stage           low space stage
	//   |--------------------|-----------------------------|-------------------------|
	//   ^                    ^                             ^                         ^
	//   0       HighSpaceRatio * capacity       LowSpaceRatio * capacity          capacity
	//
	// LowSpaceRatio is the lowest usage ratio of store which regraded as low space.
	// When in low space, store region score increases to very large and varies inversely with available size.
	LowSpaceRatio float64 `toml:"low-space-ratio,omitempty" json:"low-space-ratio"`
	// HighSpaceRatio is the highest usage ratio of store which regraded as high space.
	// High space means there is a lot of spare capacity, and store region score varies directly with used size.
	HighSpaceRatio float64 `toml:"high-space-ratio,omitempty" json:"high-space-ratio"`
	// SchedulerMaxWaitingOperator is the max coexist operators for each scheduler.
	SchedulerMaxWaitingOperator uint64 `toml:"scheduler-max-waiting-operator,omitempty" json:"scheduler-max-waiting-operator"`
	// WARN: DisableLearner is deprecated.
	// DisableLearner is the option to disable using AddLearnerNode instead of AddNode.
	DisableLearner bool `toml:"disable-raft-learner" json:"disable-raft-learner,string"`
	// DisableRemoveDownReplica is the option to prevent replica checker from
	// removing down replicas.
	DisableRemoveDownReplica bool `toml:"disable-remove-down-replica" json:"disable-remove-down-replica,string"`
	// DisableReplaceOfflineReplica is the option to prevent replica checker from
	// replacing offline replicas.
	DisableReplaceOfflineReplica bool `toml:"disable-replace-offline-replica" json:"disable-replace-offline-replica,string"`
	// DisableMakeUpReplica is the option to prevent replica checker from making up
	// replicas when replica count is less than expected.
	DisableMakeUpReplica bool `toml:"disable-make-up-replica" json:"disable-make-up-replica,string"`
	// DisableRemoveExtraReplica is the option to prevent replica checker from
	// removing extra replicas.
	DisableRemoveExtraReplica bool `toml:"disable-remove-extra-replica" json:"disable-remove-extra-replica,string"`
	// DisableLocationReplacement is the option to prevent replica checker from
	// moving replica to a better location.
	DisableLocationReplacement bool `toml:"disable-location-replacement" json:"disable-location-replacement,string"`
	// DisableNamespaceRelocation is the option to prevent namespace checker
	// from moving replica to the target namespace.
	DisableNamespaceRelocation bool `toml:"disable-namespace-relocation" json:"disable-namespace-relocation,string"`

	// Schedulers support for loading customized schedulers
	Schedulers SchedulerConfigs `toml:"schedulers,omitempty" json:"schedulers-v2"` // json v2 is for the sake of compatible upgrade

	// Only used to display
	SchedulersPayload map[string]string `json:"schedulers,omitempty"`
}

// Clone returns a cloned scheduling configuration.
func (c *ScheduleConfig) Clone() *ScheduleConfig {
	schedulers := make(SchedulerConfigs, len(c.Schedulers))
	copy(schedulers, c.Schedulers)
	return &ScheduleConfig{
		MaxSnapshotCount:             c.MaxSnapshotCount,
		MaxPendingPeerCount:          c.MaxPendingPeerCount,
		MaxMergeRegionSize:           c.MaxMergeRegionSize,
		MaxMergeRegionKeys:           c.MaxMergeRegionKeys,
		SplitMergeInterval:           c.SplitMergeInterval,
		PatrolRegionInterval:         c.PatrolRegionInterval,
		MaxStoreDownTime:             c.MaxStoreDownTime,
		MaxColdDataTime:              c.MaxColdDataTime,
		LeaderScheduleLimit:          c.LeaderScheduleLimit,
		LeaderScheduleStrategy:       c.LeaderScheduleStrategy,
		RegionScheduleLimit:          c.RegionScheduleLimit,
		ReplicaScheduleLimit:         c.ReplicaScheduleLimit,
		MergeScheduleLimit:           c.MergeScheduleLimit,
		EnableOneWayMerge:            c.EnableOneWayMerge,
		HotRegionScheduleLimit:       c.HotRegionScheduleLimit,
		HotRegionCacheHitsThreshold:  c.HotRegionCacheHitsThreshold,
		StoreBalanceRate:             c.StoreBalanceRate,
		TolerantSizeRatio:            c.TolerantSizeRatio,
		LowSpaceRatio:                c.LowSpaceRatio,
		HighSpaceRatio:               c.HighSpaceRatio,
		SchedulerMaxWaitingOperator:  c.SchedulerMaxWaitingOperator,
		DisableLearner:               c.DisableLearner,
		DisableRemoveDownReplica:     c.DisableRemoveDownReplica,
		DisableReplaceOfflineReplica: c.DisableReplaceOfflineReplica,
		DisableMakeUpReplica:         c.DisableMakeUpReplica,
		DisableRemoveExtraReplica:    c.DisableRemoveExtraReplica,
		DisableLocationReplacement:   c.DisableLocationReplacement,
		DisableNamespaceRelocation:   c.DisableNamespaceRelocation,
		Schedulers:                   schedulers,
	}
}

const (
	defaultMaxReplicas            = 3
	defaultMaxSnapshotCount       = 3
	defaultMaxPendingPeerCount    = 16
	defaultMaxMergeRegionSize     = 20
	defaultMaxMergeRegionKeys     = 200000
	defaultSplitMergeInterval     = 1 * time.Hour
	defaultPatrolRegionInterval   = 100 * time.Millisecond
	defaultMaxStoreDownTime       = 30 * time.Minute
	defaultMaxColdDataTime        = 30 * 24 * time.Hour
	defaultLeaderScheduleLimit    = 4
	defaultRegionScheduleLimit    = 2048
	defaultReplicaScheduleLimit   = 64
	defaultMergeScheduleLimit     = 8
	defaultHotRegionScheduleLimit = 4
	defaultStoreBalanceRate       = 15
	defaultTolerantSizeRatio      = 0
	defaultLowSpaceRatio          = 0.8
	defaultHighSpaceRatio         = 0.6
	// defaultHotRegionCacheHitsThreshold is the low hit number threshold of the
	// hot region.
	defaultHotRegionCacheHitsThreshold = 3
	defaultSchedulerMaxWaitingOperator = 3
	defaultLeaderScheduleStrategy      = "count"
)

func (c *ScheduleConfig) adjust(meta *configMetaData) error {
	if !meta.IsDefined("max-snapshot-count") {
		adjustUint64(&c.MaxSnapshotCount, defaultMaxSnapshotCount)
	}
	if !meta.IsDefined("max-pending-peer-count") {
		adjustUint64(&c.MaxPendingPeerCount, defaultMaxPendingPeerCount)
	}
	if !meta.IsDefined("max-merge-region-size") {
		adjustUint64(&c.MaxMergeRegionSize, defaultMaxMergeRegionSize)
	}
	if !meta.IsDefined("max-merge-region-keys") {
		adjustUint64(&c.MaxMergeRegionKeys, defaultMaxMergeRegionKeys)
	}
	adjustDuration(&c.SplitMergeInterval, defaultSplitMergeInterval)
	adjustDuration(&c.PatrolRegionInterval, defaultPatrolRegionInterval)
	adjustDuration(&c.MaxStoreDownTime, defaultMaxStoreDownTime)
	adjustDuration(&c.MaxColdDataTime, defaultMaxColdDataTime)
	if !meta.IsDefined("leader-schedule-limit") {
		adjustUint64(&c.LeaderScheduleLimit, defaultLeaderScheduleLimit)
	}
	if !meta.IsDefined("region-schedule-limit") {
		adjustUint64(&c.RegionScheduleLimit, defaultRegionScheduleLimit)
	}
	if !meta.IsDefined("replica-schedule-limit") {
		adjustUint64(&c.ReplicaScheduleLimit, defaultReplicaScheduleLimit)
	}
	if !meta.IsDefined("merge-schedule-limit") {
		adjustUint64(&c.MergeScheduleLimit, defaultMergeScheduleLimit)
	}
	if !meta.IsDefined("hot-region-schedule-limit") {
		adjustUint64(&c.HotRegionScheduleLimit, defaultHotRegionScheduleLimit)
	}
	if !meta.IsDefined("hot-region-cache-hits-threshold") {
		adjustUint64(&c.HotRegionCacheHitsThreshold, defaultHotRegionCacheHitsThreshold)
	}
	if !meta.IsDefined("tolerant-size-ratio") {
		adjustFloat64(&c.TolerantSizeRatio, defaultTolerantSizeRatio)
	}
	if !meta.IsDefined("scheduler-max-waiting-operator") {
		adjustUint64(&c.SchedulerMaxWaitingOperator, defaultSchedulerMaxWaitingOperator)
	}
	if !meta.IsDefined("leader-schedule-strategy") {
		adjustString(&c.LeaderScheduleStrategy, defaultLeaderScheduleStrategy)
	}
	adjustFloat64(&c.StoreBalanceRate, defaultStoreBalanceRate)
	adjustFloat64(&c.LowSpaceRatio, defaultLowSpaceRatio)
	adjustFloat64(&c.HighSpaceRatio, defaultHighSpaceRatio)
	adjustSchedulers(&c.Schedulers, defaultSchedulers)

	return c.Validate()
}

// Validate is used to validate if some scheduling configurations are right.
func (c *ScheduleConfig) Validate() error {
	if c.TolerantSizeRatio < 0 {
		return errors.New("tolerant-size-ratio should be nonnegative")
	}
	if c.LowSpaceRatio < 0 || c.LowSpaceRatio > 1 {
		return errors.New("low-space-ratio should between 0 and 1")
	}
	if c.HighSpaceRatio < 0 || c.HighSpaceRatio > 1 {
		return errors.New("high-space-ratio should between 0 and 1")
	}
	if c.LowSpaceRatio <= c.HighSpaceRatio {
		return errors.New("low-space-ratio should be larger than high-space-ratio")
	}
	for _, scheduleConfig := range c.Schedulers {
		if !schedule.IsSchedulerRegistered(scheduleConfig.Type) {
			return errors.Errorf("create func of %v is not registered, maybe misspelled", scheduleConfig.Type)
		}
	}
	return nil
}

// Deprecated is used to find if there is an option has beed deprecated.
func (c *ScheduleConfig) Deprecated() error {
	if c.DisableLearner {
		return errors.New("disable-raft-learner has already been deprecated")
	}
	return nil
}

// SchedulerConfigs is a slice of customized scheduler configuration.
type SchedulerConfigs []SchedulerConfig

// SchedulerConfig is customized scheduler configuration
type SchedulerConfig struct {
	Type        string   `toml:"type" json:"type"`
	Args        []string `toml:"args,omitempty" json:"args"`
	Disable     bool     `toml:"disable" json:"disable"`
	ArgsPayload string   `toml:"args-payload,omitempty" json:"args-payload"`
}

var defaultSchedulers = SchedulerConfigs{
	{Type: "balance-region"},
	{Type: "balance-leader"},
	{Type: "hot-region"},
	{Type: "label"},
	{Type: "separate-cold-hot"},
}

// IsDefaultScheduler checks whether the scheduler is enable by default.
func IsDefaultScheduler(typ string) bool {
	for _, c := range defaultSchedulers {
		if typ == c.Type {
			return true
		}
	}
	return false
}

// GetLeaderScheduleStrategy is to get leader schedule strategy
func (c *ScheduleConfig) GetLeaderScheduleStrategy() core.ScheduleStrategy {
	return core.StringToScheduleStrategy(c.LeaderScheduleStrategy)
}

// ReplicationConfig is the replication configuration.
type ReplicationConfig struct {
	// MaxReplicas is the number of replicas for each region.
	MaxReplicas uint64 `toml:"max-replicas,omitempty" json:"max-replicas"`

	// The label keys specified the location of a store.
	// The placement priorities is implied by the order of label keys.
	// For example, ["zone", "rack"] means that we should place replicas to
	// different zones first, then to different racks if we don't have enough zones.
	LocationLabels typeutil.StringSlice `toml:"location-labels,omitempty" json:"location-labels"`
	// StrictlyMatchLabel strictly checks if the label of TiKV is matched with LocationLabels.
	StrictlyMatchLabel bool `toml:"strictly-match-label,omitempty" json:"strictly-match-label,string"`
}

func (c *ReplicationConfig) clone() *ReplicationConfig {
	locationLabels := make(typeutil.StringSlice, len(c.LocationLabels))
	copy(locationLabels, c.LocationLabels)
	return &ReplicationConfig{
		MaxReplicas:        c.MaxReplicas,
		LocationLabels:     locationLabels,
		StrictlyMatchLabel: c.StrictlyMatchLabel,
	}
}

// Validate is used to validate if some replication configurations are right.
func (c *ReplicationConfig) Validate() error {
	for _, label := range c.LocationLabels {
		err := ValidateLabelString(label)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *ReplicationConfig) adjust(meta *configMetaData) error {
	adjustUint64(&c.MaxReplicas, defaultMaxReplicas)
	if !meta.IsDefined("strictly-match-label") {
		c.StrictlyMatchLabel = defaultStrictlyMatchLabel
	}
	return c.Validate()
}

// NamespaceConfig is to overwrite the global setting for specific namespace
type NamespaceConfig struct {
	// LeaderScheduleLimit is the max coexist leader schedules.
	LeaderScheduleLimit uint64 `json:"leader-schedule-limit"`
	// RegionScheduleLimit is the max coexist region schedules.
	RegionScheduleLimit uint64 `json:"region-schedule-limit"`
	// ReplicaScheduleLimit is the max coexist replica schedules.
	ReplicaScheduleLimit uint64 `json:"replica-schedule-limit"`
	// MergeScheduleLimit is the max coexist merge schedules.
	MergeScheduleLimit uint64 `json:"merge-schedule-limit"`
	// HotRegionScheduleLimit is the max coexist hot region schedules.
	HotRegionScheduleLimit uint64 `json:"hot-region-schedule-limit"`
	// MaxReplicas is the number of replicas for each region.
	MaxReplicas uint64 `json:"max-replicas"`
}

// Adjust is used to adjust the namespace configurations.
func (c *NamespaceConfig) Adjust(opt *ScheduleOption) {
	adjustUint64(&c.LeaderScheduleLimit, opt.GetLeaderScheduleLimit(namespace.DefaultNamespace))
	adjustUint64(&c.RegionScheduleLimit, opt.GetRegionScheduleLimit(namespace.DefaultNamespace))
	adjustUint64(&c.ReplicaScheduleLimit, opt.GetReplicaScheduleLimit(namespace.DefaultNamespace))
	adjustUint64(&c.MergeScheduleLimit, opt.GetMergeScheduleLimit(namespace.DefaultNamespace))
	adjustUint64(&c.HotRegionScheduleLimit, opt.GetHotRegionScheduleLimit(namespace.DefaultNamespace))
	adjustUint64(&c.MaxReplicas, uint64(opt.GetMaxReplicas(namespace.DefaultNamespace)))
}

// SecurityConfig is the configuration for supporting tls.
type SecurityConfig struct {
	// CAPath is the path of file that contains list of trusted SSL CAs. if set, following four settings shouldn't be empty
	CAPath string `toml:"cacert-path" json:"cacert-path"`
	// CertPath is the path of file that contains X509 certificate in PEM format.
	CertPath string `toml:"cert-path" json:"cert-path"`
	// KeyPath is the path of file that contains X509 key in PEM format.
	KeyPath string `toml:"key-path" json:"key-path"`
}

// ToTLSConfig generatres tls config.
func (s SecurityConfig) ToTLSConfig() (*tls.Config, error) {
	if len(s.CertPath) == 0 && len(s.KeyPath) == 0 {
		return nil, nil
	}
	tlsInfo := transport.TLSInfo{
		CertFile:      s.CertPath,
		KeyFile:       s.KeyPath,
		TrustedCAFile: s.CAPath,
	}
	tlsConfig, err := tlsInfo.ClientConfig()
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return tlsConfig, nil
}

// PDServerConfig is the configuration for pd server.
type PDServerConfig struct {
	// UseRegionStorage enables the independent region storage.
	UseRegionStorage bool `toml:"use-region-storage" json:"use-region-storage,string"`
	// MaxResetTSGap is the max gap to reset the tso.
	MaxResetTSGap time.Duration `toml:"max-reset-ts-gap" json:"max-reset-ts-gap"`
}

func (c *PDServerConfig) adjust(meta *configMetaData) error {
	if !meta.IsDefined("use-region-storage") {
		c.UseRegionStorage = defaultUseRegionStorage
	}
	if !meta.IsDefined("max-reset-ts-gap") {
		c.MaxResetTSGap = defaultMaxResetTsGap
	}
	return nil
}

// StoreLabel is the config item of LabelPropertyConfig.
type StoreLabel struct {
	Key   string `toml:"key" json:"key"`
	Value string `toml:"value" json:"value"`
}

// LabelPropertyConfig is the config section to set properties to store labels.
type LabelPropertyConfig map[string][]StoreLabel

// Clone returns a cloned label property configuration.
func (c LabelPropertyConfig) Clone() LabelPropertyConfig {
	m := make(map[string][]StoreLabel, len(c))
	for k, sl := range c {
		sl2 := make([]StoreLabel, 0, len(sl))
		sl2 = append(sl2, sl...)
		m[k] = sl2
	}
	return m
}

// ParseUrls parse a string into multiple urls.
// Export for api.
func ParseUrls(s string) ([]url.URL, error) {
	items := strings.Split(s, ",")
	urls := make([]url.URL, 0, len(items))
	for _, item := range items {
		u, err := url.Parse(item)
		if err != nil {
			return nil, errors.WithStack(err)
		}

		urls = append(urls, *u)
	}

	return urls, nil
}

// SetupLogger setup the logger.
func (c *Config) SetupLogger() error {
	lg, p, err := log.InitLogger(&c.Log, zap.AddStacktrace(zapcore.FatalLevel))
	if err != nil {
		return err
	}
	c.logger = lg
	c.logProps = p
	return nil
}

// GetZapLogger gets the created zap logger.
func (c *Config) GetZapLogger() *zap.Logger {
	return c.logger
}

// GetZapLogProperties gets properties of the zap logger.
func (c *Config) GetZapLogProperties() *log.ZapProperties {
	return c.logProps
}

// GenEmbedEtcdConfig generates a configuration for embedded etcd.
func (c *Config) GenEmbedEtcdConfig() (*embed.Config, error) {
	cfg := embed.NewConfig()
	cfg.Name = c.Name
	cfg.Dir = c.DataDir
	cfg.WalDir = ""
	cfg.InitialCluster = c.InitialCluster
	cfg.ClusterState = c.InitialClusterState
	cfg.EnablePprof = true
	cfg.PreVote = c.PreVote
	cfg.StrictReconfigCheck = !c.DisableStrictReconfigCheck
	cfg.TickMs = uint(c.TickInterval.Duration / time.Millisecond)
	cfg.ElectionMs = uint(c.ElectionInterval.Duration / time.Millisecond)
	cfg.AutoCompactionMode = c.AutoCompactionMode
	cfg.AutoCompactionRetention = c.AutoCompactionRetention
	cfg.QuotaBackendBytes = int64(c.QuotaBackendBytes)

	cfg.ClientTLSInfo.ClientCertAuth = len(c.Security.CAPath) != 0
	cfg.ClientTLSInfo.TrustedCAFile = c.Security.CAPath
	cfg.ClientTLSInfo.CertFile = c.Security.CertPath
	cfg.ClientTLSInfo.KeyFile = c.Security.KeyPath
	cfg.PeerTLSInfo.TrustedCAFile = c.Security.CAPath
	cfg.PeerTLSInfo.CertFile = c.Security.CertPath
	cfg.PeerTLSInfo.KeyFile = c.Security.KeyPath
	cfg.ForceNewCluster = c.ForceNewCluster
	cfg.ZapLoggerBuilder = embed.NewZapCoreLoggerBuilder(c.logger, c.logger.Core(), c.logProps.Syncer)
	cfg.EnableGRPCGateway = c.EnableGRPCGateway
	cfg.Logger = "zap"
	var err error

	cfg.LPUrls, err = ParseUrls(c.PeerUrls)
	if err != nil {
		return nil, err
	}

	cfg.APUrls, err = ParseUrls(c.AdvertisePeerUrls)
	if err != nil {
		return nil, err
	}

	cfg.LCUrls, err = ParseUrls(c.ClientUrls)
	if err != nil {
		return nil, err
	}

	cfg.ACUrls, err = ParseUrls(c.AdvertiseClientUrls)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}
