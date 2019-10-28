package main

import (
	"net/http"
	"os"
	"time"

	"github.com/fiatjaf/lightningd-gjson-rpc"
	"github.com/kelseyhightower/envconfig"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"gopkg.in/antage/eventsource.v1"
)

type Settings struct {
	Port       string `envconfig:"PORT" required:"true"`
	ServiceURL string `envconfig:"SERVICE_URL" required:"true"`
	SparkURL   string `envconfig:"SPARK_URL" required:"true"`
	SparkToken string `envconfig:"SPARK_TOKEN" required:"true"`
}

var err error
var s Settings
var spark *lightning.Client
var log = zerolog.New(os.Stderr).Output(zerolog.ConsoleWriter{Out: os.Stderr})
var userStreams = make(map[string]eventsource.EventSource)
var userKeys = make(map[string]string)
var withdrawalsInProcess = make(map[string]bool)

func main() {
	err = envconfig.Process("", &s)
	if err != nil {
		log.Fatal().Err(err).Msg("couldn't process envconfig.")
	}

	// spark client
	spark = &lightning.Client{
		SparkURL:              s.SparkURL,
		SparkToken:            s.SparkToken,
		DontCheckCertificates: true,
		CallTimeout:           time.Second * 60,
	}

	// routes
	setupHandlers()

	// start http server
	log.Print("listening at 0.0.0.0:" + s.Port)
	http.ListenAndServe("0.0.0.0:"+s.Port, nil)
}
