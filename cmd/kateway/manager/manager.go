package manager

// Manager is the interface that integrates with pubsub manager UI.
type Manager interface {
	// Name of the manager implementation.
	Name() string

	Start()
	Stop()

	// AuthAdmin check if an app with the key has admin rights.
	AuthAdmin(appid, pubkey string) (ok bool)

	// OwnTopic checks if an appid owns a topic.
	OwnTopic(appid, pubkey, topic string) error

	// AuthSub checks if an appid is able to consume message from hisAppid.hisTopic.
	AuthSub(appid, subkey, hisAppid, hisTopic string) error

	// LookupCluster locate the cluster name of an appid.
	LookupCluster(appid string) (cluster string, found bool)

	// IsGuardedTopic checks if a topic has retry/dead sub topics.
	IsGuardedTopic(appid, topic, ver, group string) bool
}

var Default Manager
