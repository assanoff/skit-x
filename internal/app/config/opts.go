// Package config defines the example application's configuration as a single
// go-flags options struct. Each field maps to a CLI flag (long), an environment
// variable (env, namespaced per group), a default, and a --help description.
package config

import (
	"fmt"
	"time"
)

// EnvProduction is the APP_ENV value that turns on production safety checks
// (see Validate).
const EnvProduction = "production"

// Validate rejects configurations that are unsafe for the running environment.
// It is called by the serve and migrate subcommands before they open the
// database. TLS is off by default so local development works out of the box
// (all other DB defaults point at a local Postgres); this guard makes sure that
// convenience never silently reaches production.
func (o ServerOpts) Validate() error {
	if o.Env == EnvProduction && !o.DB.TLS {
		return fmt.Errorf("refusing to run in %s with database TLS disabled: set DB_TLS=true", EnvProduction)
	}
	return nil
}

// ServerOpts is the full application configuration. Groups are namespaced so
// their env vars read as HTTP_ADDR, GRPC_ADDR, DB_USER, OTEL_ENABLED, etc.
type ServerOpts struct {
	Service  string `long:"service-name" env:"SERVICE_NAME" default:"skit-example" description:"service name"`
	Env      string `long:"app-env"      env:"APP_ENV"      default:"development"         description:"runtime environment"`
	LogLevel string `long:"log-level"    env:"LOG_LEVEL"    default:"info"                description:"log level: debug|info|warn|error"`

	HTTP        HTTP        `group:"http" namespace:"http" env-namespace:"HTTP"`
	GRPC        GRPC        `group:"grpc" namespace:"grpc" env-namespace:"GRPC"`
	Gateway     Gateway     `group:"gateway" namespace:"gateway" env-namespace:"GATEWAY"`
	Debug       Debug       `group:"debug" namespace:"debug" env-namespace:"DEBUG"`
	DB          DB          `group:"postgres" namespace:"db" env-namespace:"DB"`
	OTEL        OTEL        `group:"otel" namespace:"otel" env-namespace:"OTEL"`
	Worker      Worker      `group:"worker" namespace:"worker" env-namespace:"WORKER"`
	Broker      Broker      `group:"broker" namespace:"broker" env-namespace:"BROKER"`
	Auth        Auth        `group:"auth" namespace:"auth" env-namespace:"AUTH"`
	Webhook     Webhook     `group:"webhook" namespace:"webhook" env-namespace:"WEBHOOK"`
	Audit       Audit       `group:"audit" namespace:"audit" env-namespace:"AUDIT"`
	Translation Translation `group:"translation" namespace:"translation" env-namespace:"TRANSLATION"`
}

// HTTP holds REST server settings. REST and gRPC run together or independently:
// a server starts iff its Addr is non-empty, so an empty ADDR (e.g. HTTP_ADDR="")
// turns that transport off without a separate kill-switch flag.
type HTTP struct {
	Addr              string        `long:"addr"                env:"ADDR"                default:":8080"    description:"REST listen address (empty disables the REST server)"`
	ReadHeaderTimeout time.Duration `long:"read-header-timeout" env:"READ_HEADER_TIMEOUT" default:"5s"       description:"read header timeout"`
	RequestTimeout    time.Duration `long:"request-timeout"     env:"REQUEST_TIMEOUT"     default:"30s"      description:"per-request timeout"`
	ShutdownTimeout   time.Duration `long:"shutdown-timeout"    env:"SHUTDOWN_TIMEOUT"    default:"10s"      description:"graceful shutdown timeout"`
	BodySizeLimit     int64         `long:"body-size-limit"     env:"BODY_SIZE_LIMIT"     default:"1048576"  description:"max request body size in bytes"`
}

// GRPC holds gRPC server settings. The server starts iff Addr is non-empty
// (set GRPC_ADDR="" to run REST-only).
type GRPC struct {
	Addr            string        `long:"addr"             env:"ADDR"             default:":9090" description:"gRPC listen address (empty disables the gRPC server)"`
	ShutdownTimeout time.Duration `long:"shutdown-timeout" env:"SHUTDOWN_TIMEOUT" default:"10s"   description:"graceful shutdown timeout"`
	Reflection      bool          `long:"reflection"       env:"REFLECTION"       description:"enable server reflection"`
	MaxRecvMiB      int           `long:"max-recv-mib"     env:"MAX_RECV_MIB"     default:"16"    description:"max receive message size, MiB"`
	MaxSendMiB      int           `long:"max-send-mib"     env:"MAX_SEND_MIB"     default:"16"    description:"max send message size, MiB"`
}

// Gateway serves the grpc-gateway JSON/HTTP proxy in front of the gRPC server,
// on its own port. It starts iff Addr is non-empty AND the gRPC server is
// enabled (it proxies to GRPC.Addr); it is independent of the hand-written REST
// API and exposes the gRPC service as REST under /v1/widgets.
type Gateway struct {
	Addr string `long:"addr" env:"ADDR" default:":8081" description:"grpc-gateway listen address (empty disables it; requires the gRPC server)"`
}

// Debug serves the status endpoints (pprof + metrics + health) on its own
// internal port. Like the other servers it starts iff Addr is non-empty, so
// DEBUG_ADDR="" turns the status server off.
type Debug struct {
	Addr string `long:"addr" env:"ADDR" default:"localhost:6060" description:"status listen address — pprof, metrics, health (empty disables it)"`
}

// DB holds Postgres connection settings.
type DB struct {
	User         string `long:"user"           env:"USER"           default:"postgres"   description:"postgres user"            json:"-"`
	Password     string `long:"password"       env:"PASSWORD"       default:"postgres"   description:"postgres password"        json:"-"`
	Host         string `long:"host"           env:"HOST"           default:"localhost:5432" description:"postgres host:port"   json:"-"`
	Name         string `long:"name"           env:"NAME"           default:"skit" description:"postgres database name"   json:"-"`
	Schema       string `long:"schema"         env:"SCHEMA"         default:"public"     description:"postgres schema"          json:"-"`
	MaxIdleConns int    `long:"max-idle-conns" env:"MAX_IDLE_CONNS" default:"5"          description:"max idle connections"`
	MaxOpenConns int    `long:"max-open-conns" env:"MAX_OPEN_CONNS" default:"20"         description:"max open connections"`
	TLS          bool   `long:"tls"            env:"TLS"                                    description:"require TLS to the database; off for local dev, set DB_TLS=true in production"`
}

// Worker holds background-processing settings for the widget-import queue
// consumer. Like the servers, it is on by default and disabled via its
// kill-switch.
type Worker struct {
	Disabled      bool          `long:"disabled"       env:"DISABLED"       description:"disable background workers"`
	Interval      time.Duration `long:"interval"       env:"INTERVAL"       default:"1s" description:"queue poll interval"`
	BatchSize     int           `long:"batch-size"     env:"BATCH_SIZE"     default:"50" description:"max tasks claimed per tick"`
	CountInterval time.Duration `long:"count-interval" env:"COUNT_INTERVAL" default:"5s" description:"widget-count poller refresh interval"`
}

// Broker holds RabbitMQ + transactional-outbox settings. It is OFF by default
// (Enabled kill-switch inverted: brokers need external infra, unlike the
// servers/workers which default on). When disabled, widget.Create skips event
// publishing and no relay/consumer runs.
type Broker struct {
	Enabled    bool   `long:"enabled"     env:"ENABLED"     description:"enable RabbitMQ + outbox"`
	User       string `long:"user"        env:"USER"        default:"guest"            description:"rabbitmq user"        json:"-"`
	Password   string `long:"password"    env:"PASSWORD"    default:"guest"            description:"rabbitmq password"    json:"-"`
	Host       string `long:"host"        env:"HOST"        default:"localhost"        description:"rabbitmq host"`
	Port       string `long:"port"        env:"PORT"        default:"5672"             description:"rabbitmq port"`
	Source     string `long:"source"      env:"SOURCE"      default:"skit-example" description:"CloudEvents source"`
	Exchange   string `long:"exchange"    env:"EXCHANGE"    default:"skit.widgets" description:"widget events exchange"`
	RoutingKey string `long:"routing-key" env:"ROUTING_KEY" default:"widget.created"   description:"widget.created routing key"`
	Queue      string `long:"queue"       env:"QUEUE"       default:"skit.widget-audit" description:"consumer queue"`
}

// Auth holds JWT authentication settings for widget write endpoints. Off by
// default; when enabled a valid HMAC-signed JWT carrying RequiredRole is needed
// to create/update/delete widgets. Reads stay public.
type Auth struct {
	Enabled      bool   `long:"enabled"       env:"ENABLED"       description:"require JWT auth on widget writes"`
	JWTSecret    string `long:"jwt-secret"    env:"JWT_SECRET"    default:"" description:"HMAC secret for JWT verification" json:"-"`
	Issuer       string `long:"issuer"        env:"ISSUER"        default:"" description:"expected JWT issuer (optional)"`
	Audience     string `long:"audience"      env:"AUDIENCE"      default:"" description:"expected JWT audience (optional)"`
	RequiredRole string `long:"required-role" env:"REQUIRED_ROLE" default:"widget:write" description:"role required for widget writes"`
}

// Webhook holds the outbound widget.created webhook settings. OFF by default
// (it needs an external receiver). When enabled, an in-process eventbus consumer
// POSTs each created widget to URL using a resilient HTTP client that retries
// 429/503 with backoff (httpmw). It is independent of the broker/outbox.
type Webhook struct {
	Enabled     bool          `long:"enabled"      env:"ENABLED"      description:"POST widget.created to a webhook via the resilient HTTP client"`
	URL         string        `long:"url"          env:"URL"          default:""      description:"webhook endpoint receiving widget.created notifications"`
	Timeout     time.Duration `long:"timeout"      env:"TIMEOUT"      default:"3s"    description:"per-attempt timeout for webhook delivery"`
	MaxAttempts int           `long:"max-attempts" env:"MAX_ATTEMPTS" default:"4"     description:"max delivery attempts (retries on 429/503)"`
	BackoffBase time.Duration `long:"backoff-base" env:"BACKOFF_BASE" default:"200ms" description:"first retry delay"`
	BackoffMax  time.Duration `long:"backoff-max"  env:"BACKOFF_MAX"  default:"5s"    description:"max retry delay (cap)"`
}

// Audit holds audit-log compaction settings. Inline auto-compaction thins a
// model's history opportunistically on write (every AutoCompactEvery versions);
// the POST /auditlog/compact admin endpoint and any sweeper use the same Factor/
// KeepRecent/MaxVersions thinning over models above CompactThreshold.
type Audit struct {
	AutoCompactEvery int `long:"auto-compact-every" env:"AUTO_COMPACT_EVERY" default:"100" description:"compact a model inline every N versions (0 disables)"`
	Factor           int `long:"factor"             env:"FACTOR"             default:"4"   description:"keep every N-th middle version"`
	KeepRecent       int `long:"keep-recent"        env:"KEEP_RECENT"        default:"20"  description:"always keep the newest N versions"`
	MaxVersions      int `long:"max-versions"       env:"MAX_VERSIONS"       default:"0"   description:"cap total kept versions (0 = unlimited)"`
	CompactThreshold int `long:"compact-threshold"  env:"COMPACT_THRESHOLD"  default:"100" description:"only compact models with more than this many versions"`
	CompactLimit     int `long:"compact-limit"      env:"COMPACT_LIMIT"      default:"100" description:"max models compacted per batch call"`
}

// Translation holds settings for the per-record content-translation demo.
// Canonical widget content is authored in DefaultLang; the translationrest
// middleware translates responses into the request language (X-Language /
// Accept-Language) when a translation exists.
type Translation struct {
	DefaultLang string `long:"default-lang" env:"DEFAULT_LANG" default:"ru"    description:"default (canonical) language code"`
	Supported   string `long:"supported"    env:"SUPPORTED"    default:"ru,kk" description:"comma-separated supported language codes"`
}

// OTEL holds tracing settings.
type OTEL struct {
	Enabled     bool    `long:"enabled"     env:"ENABLED"     description:"enable OpenTelemetry tracing"`
	Endpoint    string  `long:"endpoint"    env:"ENDPOINT"    default:"localhost:4317" description:"OTLP/gRPC collector endpoint"`
	Insecure    bool    `long:"insecure"    env:"INSECURE"    description:"disable TLS to the collector"`
	Probability float64 `long:"probability" env:"PROBABILITY" default:"1.0"            description:"trace sampling probability"`
}
