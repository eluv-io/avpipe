package cmd

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/anc"
	"github.com/eluv-io/avpipe/broadcastproto/anc/venueclock"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/stamp"
	"github.com/eluv-io/common-go/media/anc"
	"github.com/eluv-io/common-go/media/anc/venueclock"
	"github.com/spf13/cobra"
)

// Anc adds the anc command: a dump of the SMPTE ST 2038 ancillary data in a transport stream. It reads the stream
// through avpipe's own demuxer, so it shows what a transcode would report in AV_IN_STAT_DATA_ANC.
func Anc(cmdRoot *cobra.Command) error {
	cmdAnc := &cobra.Command{
		Use:   "anc",
		Short: "Dump the ST 2038 ancillary data of a transport stream",
		Long: "Read an MPEG-TS file or URL (or - for stdin) and print every ancillary data packet of its ST 2038 " +
			"stream: the PES pts, the SDI line, DID/SDID, the kind and the decoded time code, with a summary at the end.",
		RunE: doAnc,
	}
	cmdRoot.AddCommand(cmdAnc)

	cmdAnc.PersistentFlags().StringP("filename", "f", "", "(mandatory) transport stream file or URL, - for stdin")
	cmdAnc.PersistentFlags().Int("anc-stream-id", 0, "(optional) stream id (TS PID) of the ancillary data stream when the PMT does not mark it")
	cmdAnc.PersistentFlags().Int("max", 0, "(optional) stop after this many PES packets")
	cmdAnc.PersistentFlags().Bool("raw", false, "(optional) print the user data words of every packet in hex")
	cmdAnc.PersistentFlags().Bool("clock", false, "(optional) reassemble the venue clock feed (DID 0x50/0x01) and print its messages and the clock state they change")
	cmdAnc.PersistentFlags().String("events-out", "", "(optional) with --clock: write the overlay events the venue clock feed makes (text slots home, away, score, period; clock slots clock and shot; pts in ms) to this JSON file, for a transcode --overlay spec")
	cmdAnc.PersistentFlags().Bool("quiet", false, "(optional) print the summary only (with --clock: the clock messages and the summary)")
	return nil
}

func doAnc(cmd *cobra.Command, args []string) error {
	filename := cmd.Flag("filename").Value.String()
	if filename == "" {
		return fmt.Errorf("Filename is needed after -f")
	}
	if filename == "-" {
		filename = "pipe:0"
	}
	opts := ancOpts{}
	var err error
	if opts.ancStreamID, err = cmd.Flags().GetInt("anc-stream-id"); err != nil {
		return err
	}
	if opts.max, err = cmd.Flags().GetInt("max"); err != nil {
		return err
	}
	if opts.raw, err = cmd.Flags().GetBool("raw"); err != nil {
		return err
	}
	if opts.clock, err = cmd.Flags().GetBool("clock"); err != nil {
		return err
	}
	if opts.eventsOut, err = cmd.Flags().GetString("events-out"); err != nil {
		return err
	}
	if opts.quiet, err = cmd.Flags().GetBool("quiet"); err != nil {
		return err
	}
	if opts.eventsOut != "" && !opts.clock {
		return fmt.Errorf("--events-out needs --clock")
	}
	return runAnc(os.Stdout, filename, opts)
}

type ancOpts struct {
	ancStreamID int
	max         int
	raw         bool
	clock       bool
	eventsOut   string
	quiet       bool
}

// errAncMax ends the scan when --max PES packets have been printed.
var errAncMax = errors.New("max reached")

// runAnc scans url and writes one line per PES packet and one per ancillary packet, then a summary.
func runAnc(w io.Writer, url string, opts ancOpts) error {
	type key struct{ did, sdid uint8 }
	var (
		nPES, nPackets, nBad, nErr int
		firstPts, lastPts          int64 = -1, -1
		byKind                           = map[key]int{}
		lastTimecode               string
		clock                      venueclock.Stream
		clockState                 venueclock.State
		nClockMsgs, nClockBadCRC   int
		eventLog                   *stamp.Log
		stamper                    *stamp.ANC
	)
	if opts.eventsOut != "" {
		eventLog = stamp.NewLog(goavpipe.KindOverlay)
		stamper = stamp.NewANC(eventLog, overlayTimescale, stamp.DefaultVenueMapping())
	}
	stream, err := avpipe.ScanANC(url, opts.ancStreamID, func(p *goavpipe.AncPacket) error {
		if stamper != nil {
			stamper.Feed(p)
		}
		nPES++
		if firstPts < 0 {
			firstPts = p.Pts
		}
		if p.Pts >= 0 {
			lastPts = p.Pts
		}
		pkts, perr := anc.ParsePES(p.Payload)
		if !opts.quiet {
			fmt.Fprintf(w, "pts=%d (%s) %d bytes, %d packets\n", p.Pts, ptsClock(p.Pts), len(p.Payload), len(pkts))
		}
		for _, pk := range pkts {
			nPackets++
			byKind[key{pk.DID, pk.SDID}]++
			if !pk.ParityOK || !pk.ChecksumOK {
				nBad++
			}
			detail := ""
			if pk.Kind() == anc.KindTimecode {
				if tc, err := anc.ParseTimecode(pk.UDW); err == nil {
					detail = " " + tc.String() + " " + tc.Source()
					lastTimecode = tc.String()
				} else {
					detail = " " + err.Error()
				}
			}
			if !opts.quiet {
				fmt.Fprintf(w, "  %s%s\n", pk, detail)
				if opts.raw {
					fmt.Fprintf(w, "    %s\n", hex.EncodeToString(pk.UDW))
				}
			}
		}
		if perr != nil {
			nErr++
			if !opts.quiet {
				fmt.Fprintf(w, "  PARSE ERROR: %v\n", perr)
			}
		}
		if opts.clock {
			msgs, cerr := clock.FeedPackets(pkts, p.Pts)
			if cerr != nil {
				fmt.Fprintf(w, "clock pts=%d (%s) MALFORMED: %v\n", p.Pts, ptsClock(p.Pts), cerr)
			}
			for _, m := range msgs {
				nClockMsgs++
				if !m.ChecksumOK {
					nClockBadCRC++
				}
				fmt.Fprintf(w, "clock pts=%d (%s) %s\n", m.Pts, ptsClock(m.Pts), m)
				if clockState.Apply(m) {
					fmt.Fprintf(w, "      state: %s\n", &clockState)
				}
			}
		}
		if opts.max > 0 && nPES >= opts.max {
			return errAncMax
		}
		return nil
	})
	if err != nil && !errors.Is(err, errAncMax) {
		if errors.Is(err, avpipe.ErrNoAncStream) {
			fmt.Fprintln(w, "no ST 2038 stream found; use --anc-stream-id to name its PID")
		}
		return err
	}

	fmt.Fprintf(w, "stream: index %d id %d (0x%x) codec %s tag %q\n", stream.StreamIndex, stream.StreamID,
		stream.StreamID, stream.CodecName, stream.CodecTag)
	fmt.Fprintf(w, "PES packets: %d, ancillary packets: %d, parity/checksum failures: %d, parse errors: %d\n",
		nPES, nPackets, nBad, nErr)
	if firstPts >= 0 {
		fmt.Fprintf(w, "pts: %d to %d (%s to %s, %.3f s)\n", firstPts, lastPts, ptsClock(firstPts), ptsClock(lastPts),
			float64(lastPts-firstPts)/90000)
	}
	if lastTimecode != "" {
		fmt.Fprintf(w, "last time code: %s\n", lastTimecode)
	}
	if opts.clock {
		fmt.Fprintf(w, "clock messages: %d, bad crc: %d, aborted: %d, malformed: %d\n", nClockMsgs, nClockBadCRC,
			clock.Aborted, clock.Malformed)
		if nClockMsgs > 0 {
			fmt.Fprintf(w, "clock state: %s\n", &clockState)
		}
	}
	if stamper != nil {
		events := eventLog.Events(goavpipe.KindOverlay)
		b, err := json.MarshalIndent(events, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(opts.eventsOut, append(b, '\n'), 0644); err != nil {
			return err
		}
		fmt.Fprintf(w, "overlay events: %d written to %s (venue clock messages %d, events %d, bad crc %d)\n",
			len(events), opts.eventsOut, stamper.Stats.Messages, stamper.Stats.Events, stamper.Stats.BadCRC)
	}
	keys := make([]key, 0, len(byKind))
	for k := range byKind {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].did != keys[b].did {
			return keys[a].did < keys[b].did
		}
		return keys[a].sdid < keys[b].sdid
	})
	for _, k := range keys {
		fmt.Fprintf(w, "  DID 0x%02x SDID 0x%02x %-14s %d\n", k.did, k.sdid, anc.KindOf(k.did, k.sdid), byKind[k])
	}
	return nil
}

// ptsClock formats a 90 kHz pts as h:mm:ss.mmm.
func ptsClock(pts int64) string {
	if pts < 0 {
		return "no pts"
	}
	ms := pts / 90
	return fmt.Sprintf("%d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
