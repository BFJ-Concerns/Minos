package heartbeat

import (
	"flag"
	"fmt"
	"io"
	"time"
)

func WriteCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("heartbeat", flag.ContinueOnError)
	path := flags.String("path", "/var/lib/minos/heartbeat.json", "heartbeat output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: minos heartbeat [--path file]")
	}
	if err := Write(*path, time.Now()); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, "heartbeat written")
	return err
}

func CheckCommand(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("heartbeat-check", flag.ContinueOnError)
	path := flags.String("path", "/var/lib/minos/heartbeat.json", "heartbeat path")
	maximumAge := flags.Duration("max-age", 3*time.Minute, "maximum permitted heartbeat age")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *maximumAge <= 0 {
		return fmt.Errorf("usage: minos heartbeat-check [--path file] [--max-age duration]")
	}
	if err := Check(*path, time.Now(), *maximumAge); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, "heartbeat healthy")
	return err
}
