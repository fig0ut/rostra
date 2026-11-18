package main

import (
	"context"
	"os"
	"time"

	C "github.com/fig0ut/rostra/constant"
	"github.com/fig0ut/rostra/log"

	"github.com/sagernet/sing/common"

	"github.com/spf13/cobra"
)

const serviceName = "sing-box-daemon"

var mainCommand = &cobra.Command{
	Use:     serviceName,
	Version: C.Version,
}

func main() {
	logFactory := log.NewDefaultFactory(context.Background(), log.Formatter{
		BaseTime:      time.Now(),
		DisableColors: true,
	}, os.Stderr, "", nil, false)
	common.Must(logFactory.Start())
	log.SetStdLogger(logFactory.Logger())
	err := mainCommand.Execute()
	if err != nil {
		log.Fatal(err)
	}
}
