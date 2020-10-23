package linear

import (
	"fmt"
	"math"
	"time"
)

type PlayableType int

const (
	VodPlayable PlayableType = iota
	LivePlayable
)

// SignalSource objects
type SignalSource struct {
	qhash     string
	startPts  int64
	timescale int64
}

type MediaSlice struct {
	QHash      string
	Offering   string
	StartPts   int64
	DurationTs int64
	Type       PlayableType
	mediaInfo  MediaInfo
	trigCtx    TriggerCtx
}

// MediaInfo describes the content's media (like `media_struct`)
// This information is normally read from the content's media_struct and this is a
// stub to allow the unit test to work independently
type MediaInfo struct {
	videoTimescale int64
}

// TriggerCtx records the all the necessary information about what
// triggered the beginning and end of a media slice
type TriggerCtx struct {
	extPtsStart, extPtsEnd int64 // If triggered by another content's PTS signal
	extTimescale           int64 // Timescale of external content
}

type MediaSequence struct {
	Slices []*MediaSlice
}

func (msq *MediaSequence) String() string {
	str := "MEDIASEQ\n"
	for _, msl := range msq.Slices {
		str = str + fmt.Sprintf("%v %v %d %d \n", msl.QHash, msl.Offering, msl.StartPts, msl.DurationTs)
	}
	return str
}

type Event struct {
	trigger    Trigger
	conditions []Condition

	numFired int // For the most each event fires only once
}

// Fired returns true if the event is triggered and the conditions are met
func (e *Event) Fired(ptsCursor int64, actx *AudienceContext) *TriggerInfo {
	//if e.numFired >= 1 {
	//return nil // Currently all events are single fire
	//}
	if ti := e.trigger.IsTriggered(ptsCursor); ti != nil {
		//fmt.Println("Event triggered", e.trigger)
		for _, c := range e.conditions {
			if c.Eval(actx) == false {
				return nil
			}
		}
		e.numFired++
		//fmt.Println("Event fired", e, "numFired", e.numFired)
		return ti
	}
	return nil
}

type TriggerInfo struct {
	Kind         SignalKind
	Source       *SignalSource
	Pts          int64
	EpochNanoGmt int64
}

type Trigger interface {
	// IsTriggered returns a pointer to the trigger info or nil if the trigger didn't occur
	IsTriggered(ptsCursor int64) *TriggerInfo
}

type TriggerWallClockTime struct {
	epochNanoGmt int64
}

func (t TriggerWallClockTime) IsTriggered(_ int64) *TriggerInfo {
	if time.Now().UnixNano() >= t.epochNanoGmt {
		return &TriggerInfo{Kind: WallClock, EpochNanoGmt: t.epochNanoGmt}
	}
	return nil
}

type TriggerScte35 struct {
	source *SignalSource
	id     int
	sr     *SignalRecorder
	adjust int64 // add this to the PTS in the SCTE35 event
}

func (t TriggerScte35) IsTriggered(ptsCursor int64) *TriggerInfo {
	sp := &Signal{Kind: Scte35, Source: t.source, Id: t.id}
	fmt.Println("TriggerScte35 - finding", sp)
	sig := t.sr.Find(sp, ptsCursor) // the events have to be consumed in order - they are NOT; is the NEXT event this one
	//sig := t.sr.FindNext(t.sr.idx)
	//t.sr.idx++
	//if sp == sig {
	//	fmt.Println("TriggerScte35 - Next signal matches", sp)
	//	return &TriggerInfo{Kind: Scte35, Source: t.source, Pts: sig.Pts + t.adjust}
	//}
	if sig != nil {
		return &TriggerInfo{Kind: Scte35, Source: t.source, Pts: sig.Pts + t.adjust}
	}
	return nil
}

type TriggerPts struct {
	source *SignalSource
	pts    int64
}

func (t TriggerPts) IsTriggered(_ int64) *TriggerInfo {
	fmt.Println("TriggerPts - finding", t.source, t.pts)
	return &TriggerInfo{Kind: Pts, Source: t.source, Pts: t.pts}
}

type TriggerByUser struct {
	tags string // User can tag the event to match desired rule
}

type Condition interface {
	Eval(actx *AudienceContext /* Should be a superset of more contexts */) bool
}

type Context struct {
	startPts        int64 // Set if, for example, resuming a playable
	durationTs      int64 // Set if we know ahead of time how long the slice will be
	sourceTimescale int64 // If signaled by SCTE or PTS of a base source
	media           *MediaContext
	audience        *AudienceContext
}

type MediaContext struct {
	id                 string
	categories         []string // tags describing the category
	programTitle       string
	programDescription string
	episodeTitle       string
	episodeDescription string
	rating             string // Should be an enum
}

type AudienceContext struct {
	Interests []string // tags matching media category

	// Policy results
	Blackout         bool
	BlackoutDevice   bool
	BlackoutLocation bool
}

type Playable interface {
	// PlaySpec returns the MediaSequence to be played - largely a single MediaSlice
	// but could be several Slices for example when the Playable decides to play two
	// shorter ads in a given slot.
	PlaySpec(ctx *Context) (*MediaSequence, error)
}

type PlayableSingleOnDemand struct {
	qhash      string
	timescale  int64
	durationTs int64
}

type PlayableSingleOnDemandResume struct {
	qhash      string
	timescale  int64
	resume_pts int64
}

type PlayableSingleLiveStream struct {
	qhash     string
	timescale int64
}

type PlayableAdsPool struct {
	adsHashes []string
	timescale int64
}

type PlayableAdsManager struct {
	adsManager string // PENDING - this can be an adsmanager content object with bitcode
}

type Rule struct {
	event    Event
	playable Playable
}

// Used to match rules in a sequence
type RuleSequence struct {
}

// ScheduleContext is a separate structure as it will likely come from metadata JSON
type ScheduleContext struct {
	playoutType string // "live" or "ondemand"

	startWallClockTime int64 // epoch nano GMT
}

type Schedule struct {
	rules             []*Rule
	rules_unscheduled []*Rule
}

func (s *Schedule) MakeSequence(actx *AudienceContext, baseStartPts int64) (*MediaSequence, error) {

	mseq := MediaSequence{Slices: []*MediaSlice{}}

	var crtPlayableTriggeredByScte35Pts int64 // If the current playable is triggered by SCTE35

	for _, r := range s.rules {

		if ti := r.event.Fired(crtPlayableTriggeredByScte35Pts+1, actx); ti != nil {
			fmt.Println("MAKESEQ - fired", "kind", ti, "cursor_pts", crtPlayableTriggeredByScte35Pts+1)

			// Before we make a new scheduled slice check if there is an unscheduled event
			// with an earlier PTS and if so, make its slice, resume to the current slice, and then proceed with this new event
			currentPts := ti.Pts

			for _, ri := range s.rules_unscheduled {

				if ti_unscheduled := ri.event.Fired(crtPlayableTriggeredByScte35Pts+1, actx); ti != nil {

					if ti_unscheduled.Pts <= currentPts {
						fmt.Println("MAKESEQ - unscheduled event triggered", "kind", ti_unscheduled,
							"event PTS", ti_unscheduled.Pts, "current event PTS", currentPts, "cursor_pts", crtPlayableTriggeredByScte35Pts+1)

						// Set the resume_pts = crtPlayableTrggeredbyScte35Pts; this will be used to resume
						// in the currently playing slice after the unscheduled event is over
						var resumePts int64

						// Set the end pts of the currently playing slice and its duration
						if ti_unscheduled.Kind == Scte35 {
							/* If current slice is triggered by SCTE35, or current slice is the
							 * source of the SCTE35 trigger, set the end PTS (duration)
							 */
							if crtPlayableTriggeredByScte35Pts >= 0 ||
								(ti_unscheduled.Source != nil && mseq.Slices[len(mseq.Slices)-1].QHash == ti_unscheduled.Source.qhash) {

								endPts := ti_unscheduled.Pts

								mseq.Slices[len(mseq.Slices)-1].SetExtPtsEnd(endPts, ti_unscheduled.Source.timescale)
								fmt.Println("MAKESEQ - set current slice duration (unsch)", mseq.Slices[len(mseq.Slices)-1].DurationTs)
							}
							resumePts = crtPlayableTriggeredByScte35Pts + mseq.Slices[len(mseq.Slices)-1].DurationTs
							crtPlayableTriggeredByScte35Pts = ti_unscheduled.Pts
						} else {
							crtPlayableTriggeredByScte35Pts = -1
						}

						// Set the sourceTimescale for this new source
						sourceTimescale := int64(-1)
						if ti_unscheduled.Source != nil {
							sourceTimescale = ti.Source.timescale
						}
						// Create a playable.PlaySpec to create the new slice for the unscheduled content and append

						/* At this time we don't yet know the duration - it will be set when we process the next event */
						msli, _ := ri.playable.PlaySpec(&Context{startPts: 0, durationTs: -1, sourceTimescale: sourceTimescale})

						if ti_unscheduled.Kind == Scte35 {
							msli.Slices[0].SetExtPtsStart(ti_unscheduled.Pts)
						}

						/* If the next slice is a return to the source of the SCTE signal, set StartPts */
						if msli.Slices != nil && len(msli.Slices) >= 1 && ti_unscheduled.Source != nil &&
							msli.Slices[0].QHash == ti_unscheduled.Source.qhash {
							msli.Slices[0].StartPts = ti_unscheduled.Pts - ti_unscheduled.Source.startPts // Offset by the input's start PTS
						}

						fmt.Println("MAKESEQ - new slice", msli.Slices[0])
						mseq.Slices = append(mseq.Slices, msli.Slices...)

						// Construct the resumable slice by copying what was the currently playing slice and update its start pts

						// and remove its duration (which will be updated by the next event, and append)

						slice := *mseq.Slices[len(mseq.Slices)-2]
						slice.StartPts = resumePts - baseStartPts // (8553957321 + 331984) // adjust for start PTS of base stream
						slice.DurationTs = -1
						fmt.Println("MAKESEQ - resume slice", slice)
						mseq.Slices = append(mseq.Slices, &slice)
					} else {
						fmt.Println("MAKESEQ - unscheduled event NOT triggered yet", "kind", ti_unscheduled, "event PTS",
							ti_unscheduled.Pts, "current event PTS", currentPts, "cursor_pts", crtPlayableTriggeredByScte35Pts+1)
					}
				}
			}

			if ti.Kind == Scte35 {
				/* If current slice is triggered by SCTE35, or current slice is the
				 * source of the SCTE35 trigger, set the end PTS (duration)
				 */
				endPts := ti.Pts
				mseq.Slices[len(mseq.Slices)-1].SetExtPtsEnd(endPts, ti.Source.timescale)
				fmt.Println("MAKESEQ - set current slice duration", mseq.Slices[len(mseq.Slices)-1].DurationTs)

				crtPlayableTriggeredByScte35Pts = ti.Pts
			} else {
				crtPlayableTriggeredByScte35Pts = -1
			}

			sourceTimescale := int64(-1)
			if ti.Source != nil {
				sourceTimescale = ti.Source.timescale
			}

			/* At this time we don't yet know the duration - it will be set when we process the next event */
			msli, _ := r.playable.PlaySpec(&Context{startPts: 0, durationTs: -1, sourceTimescale: sourceTimescale})
			if ti.Kind == Scte35 {
				msli.Slices[0].SetExtPtsStart(ti.Pts)
			} else if ti.Kind == WallClock {
				// This is only for the first slice in the sequence - assumed to be the signal source
				msli.Slices[0].SetExtPtsStart(baseStartPts /*8553957321 + 331984*/) // PENDING
			}

			/* If the next slice is a return to the source of the SCTE signal, set StartPts */
			if msli.Slices != nil && len(msli.Slices) >= 1 && ti.Source != nil &&
				msli.Slices[0].QHash == ti.Source.qhash {
				msli.Slices[0].StartPts = ti.Pts - ti.Source.startPts // Offset by the input's start PTS
			}
			fmt.Println("MAKESEQ - new slice", msli.Slices[0])
			mseq.Slices = append(mseq.Slices, msli.Slices...)
		}
	}

	return &mseq, nil
}

type SignalKind int

const (
	WallClock SignalKind = 1 + iota
	Pts
	Scte35
)

// Signal represents an external signal/event that occurs asynchronously and
// is recoreded for future processing by the scheduler engine
// PENDING - this might need to be subclassed for specific signals
type Signal struct {
	Kind         SignalKind
	Source       *SignalSource
	Id           int // For example for SCTE35
	Msg          string
	Pts          int64
	EpochNanoGmt int64
}

// EventRecorder keeps an unstructured record of all the events occurred externally
type SignalRecorder struct {
	signals      map[SignalKind][]*Signal // mapped by 'kind'
	signals_list []*Signal                // list of all signals in PTS order
	idx          int64
}

// RecordEvent records an event ocurred externally
func (sr *SignalRecorder) Record(s *Signal) {
	// PENDING - truncate the beginning of the array
	sr.signals[s.Kind] = append(sr.signals[s.Kind], s)
	sr.signals_list = append(sr.signals_list, s) // we need a list of these in PTS order
}

// Find looks for a signal matching a signal pattern (after a certain cursor)
func (sr *SignalRecorder) Find(sp *Signal, ptsCursor int64) *Signal {
	ss, ok := sr.signals[sp.Kind]
	if !ok {
		return nil
	}

	wrapAdjust := int64(0)
	prevPts := int64(0)

	for _, si := range ss {

		// Check for PTS wrap
		if si.Pts < prevPts {
			wrapAdjust = int64(math.Pow(2, 33))
		}
		prevPts = si.Pts

		if si.Pts+wrapAdjust <= ptsCursor {
			fmt.Println("Find - current pts less than cursor ", si.Pts, ptsCursor)
			continue
		}
		if si.Source == sp.Source &&
			si.Id == sp.Id &&
			si.Msg == sp.Msg {
			fmt.Println("Find", "signal", si, "cursor", ptsCursor)
			return si
		}
	}
	return nil
}

func (sr *SignalRecorder) FindNext(idx int64) *Signal {

	fmt.Println("FindNext ", idx)
	si := sr.signals_list[idx]
	return si

}

// Playables
func (p PlayableSingleOnDemand) PlaySpec(ctx *Context) (*MediaSequence, error) {

	startPts := ctx.startPts
	durationTs := ctx.durationTs
	if p.timescale > 0 && ctx.sourceTimescale > 0 {
		startPts = ctx.startPts * p.timescale / ctx.sourceTimescale
		durationTs = ctx.durationTs * p.timescale / ctx.sourceTimescale
	}

	if p.durationTs > 0 { // Use the duration specified in the playable object if present
		durationTs = p.durationTs
	}

	if durationTs == 0 {
		durationTs = -1
	}

	msl := &MediaSlice{
		QHash:      p.qhash,
		Offering:   "default",
		StartPts:   startPts,
		DurationTs: durationTs,
		Type:       VodPlayable,
		mediaInfo:  MediaInfo{videoTimescale: p.timescale},
		trigCtx:    TriggerCtx{extTimescale: ctx.sourceTimescale},
	}
	return &MediaSequence{Slices: []*MediaSlice{msl}}, nil
}

func (p PlayableSingleOnDemandResume) PlaySpec(ctx *Context) (*MediaSequence, error) {

	startPts := ctx.startPts
	durationTs := ctx.durationTs
	if p.timescale > 0 && ctx.sourceTimescale > 0 {
		startPts = ctx.startPts * p.timescale / ctx.sourceTimescale
		durationTs = ctx.durationTs * p.timescale / ctx.sourceTimescale
	}

	msl := &MediaSlice{
		QHash:      p.qhash,
		Offering:   "default",
		StartPts:   startPts,
		DurationTs: durationTs,
		Type:       VodPlayable,
		mediaInfo:  MediaInfo{videoTimescale: p.timescale},
		trigCtx:    TriggerCtx{extTimescale: ctx.sourceTimescale},
	}
	return &MediaSequence{Slices: []*MediaSlice{msl}}, nil
}

func (p PlayableSingleLiveStream) PlaySpec(ctx *Context) (*MediaSequence, error) {

	startPts := ctx.startPts
	durationTs := ctx.durationTs
	/*
		if p.Timescale > 0 && ctx.sourceTimescale > 0 {
			StartPts = ctx.StartPts * p.Timescale / ctx.sourceTimescale
			DurationTs = ctx.DurationTs *  p.Timescale / ctx.sourceTimescale
		}
	*/
	msl := &MediaSlice{
		QHash:      p.qhash,
		Offering:   "default",
		StartPts:   startPts,
		DurationTs: durationTs,
		Type:       LivePlayable,
		mediaInfo:  MediaInfo{videoTimescale: p.timescale},
		trigCtx:    TriggerCtx{extTimescale: ctx.sourceTimescale},
	}
	return &MediaSequence{Slices: []*MediaSlice{msl}}, nil
}

func (p PlayableAdsPool) PlaySpec(ctx *Context) (*MediaSequence, error) {
	return nil, nil
}

func (msl *MediaSlice) SetExtPtsStart(pts int64) {
	msl.trigCtx.extPtsStart = pts
}

// The extTimescale is only used for the first slice of the sequence which is not started by
// a scte trigger but is ended by one.
func (msl *MediaSlice) SetExtPtsEnd(pts int64, extTimescale int64) {
	c := &msl.trigCtx

	c.extPtsEnd = pts

	if c.extTimescale == -1 && extTimescale > 0 {
		c.extTimescale = extTimescale
	}

	if c.extTimescale > 0 {
		msl.DurationTs = (c.extPtsEnd - c.extPtsStart) * msl.mediaInfo.videoTimescale / c.extTimescale
	} else {
		msl.DurationTs = c.extPtsEnd - c.extPtsStart
	}
}
