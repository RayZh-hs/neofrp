package main

import (
	"flag"
	"fmt"
	"os"

	"neofrp/client"
	C "neofrp/common/constant"
	"neofrp/common/parser"

	"github.com/charmbracelet/log"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "c", "", "Client configuration file")
	flag.Parse()

	if configFile == "" {
		fmt.Println("Usage: frpc -c <config_file>")
		os.Exit(1)
	}

	config, err := parser.ParseClientConfig(configFile)
	if err != nil {
		log.Errorf("Failed to parse config file: %v", err)
		os.Exit(1)
	}

	if err := parser.ValidateClientConfig(config); err != nil {
		log.Errorf("Invalid client config: %v\n", err)
		os.Exit(1)
	}

	log.Infof("Parsed client config")
	logLevel := config.LogConfig.LogLevel
	if logLevel == "" || C.LogLevelMap[logLevel] == 0 {
		log.Warnf("Using default log level INFO")
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(C.LogLevelMap[logLevel])
	}

	// Run the client service
	client.Run(config)
}
