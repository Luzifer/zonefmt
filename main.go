package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Luzifer/rconfig"
	log "github.com/sirupsen/logrus"
)

var (
	cfg = struct {
		SpecialSort    string `flag:"sort,s" default:"SOA=0,NS=10,MX=20" description:"Custom sorting preferences, unspecified entries are scored 100"`
		WriteBack      bool   `flag:"write-file,w" default:"false" description:"Write back into origin file instead of stdout"`
		VersionAndExit bool   `flag:"version" default:"false" description:"Prints current version and exits"`
	}{}

	sorting = map[string]uint64{
		"SOA": 0,
		"NS":  10,
		"MX":  20,
	}

	version = "dev"
)

func init() {
	if err := rconfig.Parse(&cfg); err != nil {
		log.Fatalf("Unable to parse commandline options: %s", err)
	}

	if cfg.VersionAndExit {
		fmt.Printf("zonefmt %s\n", version)
		os.Exit(0)
	}

	for _, scor := range strings.Split(cfg.SpecialSort, ",") {
		if scor == "" {
			continue
		}

		parts := strings.SplitN(scor, "=", 2)
		s, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			log.WithFields(log.Fields{"entry": scor}).WithError(err).Fatal("Scoring entry broken")
		}
		sorting[parts[0]] = s
	}
}

func main() {
	for _, zf := range rconfig.Args()[1:] {
		zoneFile, err := os.Open(zf)
		if err != nil {
			log.WithError(err).Fatal("Unable to open zone file for read")
		}

		zoneResult, err := formatZone(zoneFile)
		if err != nil {
			log.WithError(err).Fatal("Unable to format zone file")
		}

		zoneFile.Close()

		var out io.Writer = os.Stdout
		if cfg.WriteBack {
			f, err := os.Create(zf)
			if err != nil {
				log.WithError(err).Fatal("Unable to open zone file for write")
			}
			defer f.Close()
			out = f
		}

		io.Copy(out, zoneResult)
	}
}

type record struct {
	Name  string
	TTL   int
	Class string
	Type  string
	Data  string
}

type records []record

func (r records) Len() int      { return len(r) }
func (r records) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r records) Less(i, j int) bool {
	var (
		sci, scj uint64 = 100, 100
	)
	if sc, ok := sorting[r[i].Type]; ok {
		sci = sc
	}
	if sc, ok := sorting[r[j].Type]; ok {
		scj = sc
	}

	// Use scoring if it's different
	if sci != scj {
		return sci < scj
	}

	// Same type, sort by name
	return r[i].Name < r[j].Name
}

func formatZone(zoneFile io.Reader) (io.Reader, error) {
	var (
		zone string
		ttl  int
	)

	rr := []record{}

	scanner := bufio.NewScanner(zoneFile)
	for scanner.Scan() {
		if scanner.Text()[0] == '$' {
			parts := strings.SplitN(scanner.Text(), " ", 2)
			switch parts[0] {
			case "$ORIGIN":
				zone = parts[1]
			case "$TTL":
				t, err := time.ParseDuration(parts[1])
				if err != nil {
					return nil, err
				}
				ttl = int(t.Seconds())
			default:
				log.WithFields(log.Fields{"line": scanner.Text()}).Warn("Unknown directive")
			}

			continue
		}

		rec, err := parseRecord(scanner.Text(), zone, ttl)
		if err != nil {
			log.WithError(err).Error("Unparsable record, ignoring")
			continue
		}
		rr = append(rr, rec)
	}

	sort.Sort(records(rr))

	var maxLen int
	for _, r := range rr {
		if l := len(r.Name); l > maxLen {
			maxLen = l
		}
	}

	buf := new(bytes.Buffer)
	for _, r := range rr {
		fmt.Fprintf(buf, "%-"+strconv.Itoa(maxLen)+"s %5d %s %-5s %s\n",
			r.Name,
			r.TTL,
			r.Class,
			r.Type,
			r.Data,
		)
	}

	return buf, nil
}

func parseRecord(line, defaultZone string, defaultTTL int) (record, error) {
	rex := regexp.MustCompile(`^(\S+)\s+(?:([0-9]+)\s+)?(\S+)\s+(\S+)\s+(.*)$`)
	matches := rex.FindStringSubmatch(line)

	var (
		err error
		ttl = int64(defaultTTL)
	)
	if matches[2] != "" {
		if ttl, err = strconv.ParseInt(matches[2], 10, 64); err != nil {
			return record{}, err
		}
	}

	rec := record{
		Name:  matches[1],
		TTL:   int(ttl),
		Class: matches[3],
		Type:  matches[4],
		Data:  matches[5],
	}

	if rec.Name == "@" {
		// Will get replaced in next step
		rec.Name = ""
	}

	if !strings.HasSuffix(rec.Name, ".") {
		rec.Name = strings.TrimLeft(strings.Join([]string{rec.Name, defaultZone}, "."), ".")
	}

	if rec.TTL == 0 {
		rec.TTL = defaultTTL
	}

	if rec.Type == "TXT" && rec.Data[0] != '"' {
		rec.Data = fmt.Sprintf("%q", rec.Data)
	}

	return rec, nil
}
