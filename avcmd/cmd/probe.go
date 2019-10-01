package cmd

import (
	"fmt"

	"github.com/qluvio/avpipe"
	"github.com/spf13/cobra"
)

func Probe(cmdRoot *cobra.Command) error {
	cmdProbe := &cobra.Command{
		Use:   "probe",
		Short: "Probe a media file",
		Long:  "Probe a media file and print the result",
		RunE:  doProbe,
	}

	cmdRoot.AddCommand(cmdProbe)

	cmdProbe.PersistentFlags().StringP("filename", "f", "", "(mandatory) filename to be probed")
	cmdProbe.PersistentFlags().BoolP("seekable", "", false, "(optional) seekable stream")

	return nil
}

func doProbe(cmd *cobra.Command, args []string) error {

	filename := cmd.Flag("filename").Value.String()
	if len(filename) == 0 {
		return fmt.Errorf("Filename is needed after -f")
	}

	seekable, err := cmd.Flags().GetBool("seekable")
	if err != nil {
		return fmt.Errorf("Invalid seekable flag")
	}
	log.Debug("doProbe", "seekable", seekable, "filename", filename)

	avpipe.InitIOHandler(&avcmdInputOpener{url: filename}, &avcmdOutputOpener{dir: ""})

	probeInfos, err := avpipe.Probe(filename, seekable)
	if err != nil {
		return fmt.Errorf("Probing failed. file=%s", filename)
	}

	for i, probeInfo := range probeInfos {
		fmt.Printf("Stream[%d]\n", i)
		fmt.Printf("\tcodec_type: %s\n", avpipe.AVMediaTypeName(probeInfo.CodecType))
		fmt.Printf("\tcodec_id: %d\n", probeInfo.CodecID)
		fmt.Printf("\tcodec_name: %s\n", probeInfo.CodecName)
		fmt.Printf("\tduration_ts: %d\n", probeInfo.DurationTs)
		fmt.Printf("\ttime_base: %d/%d\n", probeInfo.TimeBase.Num(), probeInfo.TimeBase.Denom())
		fmt.Printf("\tnb_frames: %d\n", probeInfo.NBFrames)
		fmt.Printf("\tstart_time: %d\n", probeInfo.StartTime)
		fmt.Printf("\tavg_frame_rate: %d/%d\n", probeInfo.AvgFrameRate.Num(), probeInfo.AvgFrameRate.Denom())
		fmt.Printf("\tframe_rate: %d/%d\n", probeInfo.FrameRate.Num(), probeInfo.FrameRate.Denom())
		fmt.Printf("\tticks_per_frame: %d\n", probeInfo.TicksPerFrame)
		fmt.Printf("\tbit_rate: %d\n", probeInfo.BitRate)
		fmt.Printf("\thas_b_frames: %v\n", probeInfo.Has_B_Frames)
		fmt.Printf("\twidth: %d\n", probeInfo.Width)
		fmt.Printf("\theight: %d\n", probeInfo.Height)
		if probeInfo.PixFmt >= 0 {
			fmt.Printf("\tpix_fmt: %d\n", probeInfo.PixFmt)
		} else {
			fmt.Printf("\tpix_fmt: -\n")
		}
		fmt.Printf("\tsample_aspect_ratoi: %d/%d\n", probeInfo.SampleAspectRatio.Num(), probeInfo.SampleAspectRatio.Denom())
		fmt.Printf("\tdisplay_aspect_ratoi: %d/%d\n", probeInfo.DisplayAspectRatio.Num(), probeInfo.DisplayAspectRatio.Denom())
		fmt.Printf("\tfield_order: %d\n", probeInfo.FieldOrder)
	}

	return nil
}
