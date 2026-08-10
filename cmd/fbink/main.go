package main

import (
	"encoding/binary"
	"fmt"
	"kobo-anki/core"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/open-spaced-repetition/go-fsrs/v3"
	"golang.org/x/image/math/fixed"
)

// ============================================================
// GUI framework: Rect, scene, drawing primitives
// ============================================================

// Rect is a pixel rectangle on screen.
type Rect struct{ X, Y, W, H int }

func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Element is a named touchable area.
type Element struct {
	ID   string
	Rect Rect
}

var scene []Element

func sceneClear()                { scene = scene[:0] }
func sceneAdd(id string, r Rect) { scene = append(scene, Element{id, r}) }

// sceneHitTest returns the ID of the last-added element containing (x,y).
func sceneHitTest(x, y int) string {
	for i := len(scene) - 1; i >= 0; i-- {
		if scene[i].Rect.Contains(x, y) {
			return scene[i].ID
		}
	}
	return ""
}

// rectPct builds a Rect from screen percentages.
func rectPct(xPct, yPct, wPct, hPct int) Rect {
	return Rect{
		screenW * xPct / 100,
		screenH * yPct / 100,
		screenW * wPct / 100,
		screenH * hPct / 100,
	}
}

// splitH splits r into n horizontal columns with a pixel gap between them.
func splitH(r Rect, n, gap int) []Rect {
	totalGap := gap * (n - 1)
	w := (r.W - totalGap) / n
	out := make([]Rect, n)
	for i := range out {
		out[i] = Rect{r.X + i*(w+gap), r.Y, w, r.H}
	}
	return out
}

// splitV splits r into n vertical rows with a pixel gap between them.
func splitV(r Rect, n, gap int) []Rect {
	totalGap := gap * (n - 1)
	h := (r.H - totalGap) / n
	out := make([]Rect, n)
	for i := range out {
		out[i] = Rect{r.X, r.Y + i*(h+gap), r.W, h}
	}
	return out
}

// Alignment for text within a rect.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// inset shrinks a rect by d pixels on each side.
func inset(r Rect, d int) Rect {
	return Rect{r.X + d, r.Y + d, r.W - 2*d, r.H - 2*d}
}

// ============================================================
// Drawing primitives
//
// These draw into an in-memory canvas (canvas.go) rather than shelling out
// to fbink per element - a whole screen is composed here, then flushed to
// the e-ink panel with a single fbink blit + refresh (flushScreen, in
// pgm.go) at the end of each draw*Screen function. Signatures are
// unchanged from the old fbink-CLI-per-call version so call sites below
// (drawButton, drawLabel, draw*Screen) didn't need to change.
// ============================================================

// fbinkFillRect fills a rectangle with a color.
func fbinkFillRect(r Rect, color string) {
	canvas.FillRect(r, colorToGray(color))
}

// fbinkTextRect draws plain (single-style) text inside a rect, word-wrapping
// to fit r's width. If centerV, the wrapped block is vertically centered
// within r using its real post-wrap height, rather than r.Y directly.
func fbinkTextRect(r Rect, text string, ft FontType, size int, color string, align Align, centerV bool) {
	resolve := makeFaceResolver(ft, size)
	regular, err := resolve(StyleRegular)
	if err != nil {
		if debug {
			fmt.Printf("fbinkTextRect: %v\n", err)
		}
		return
	}

	words := splitPlainWords(text)
	lines, err := wrapWords(words, resolve, fixed.I(r.W))
	if err != nil {
		if debug {
			fmt.Printf("fbinkTextRect: wrap error: %v\n", err)
		}
		return
	}

	lineHeight := scaledLineHeight(regular.Face)
	ascent := faceAscent(regular.Face)
	target := r
	if centerV {
		target = centerRectV(r, len(lines)*lineHeight)
	}
	drawLines(canvas, lines, target, resolve, colorToGray(color), lineHeight, ascent, align)
}

// centerRectV returns r shifted down so content of height contentH is
// vertically centered within it, using the real rendered height (known
// only after wrapping) rather than a guess from point size - replaces the
// old vcenter() heuristic, which estimated height as size*2 treating
// points as pixels; that was a rough-enough approximation at the wrong
// hardcoded 96 DPI this app used to render at, but badly undershoots now
// that text renders at its correct (larger) physical size.
func centerRectV(r Rect, contentH int) Rect {
	offset := (r.H - contentH) / 2
	if offset < 0 {
		offset = 0
	}
	return Rect{r.X, r.Y + offset, r.W, r.H - offset}
}

// drawButton draws a filled button and registers it as a touch target.
func drawButton(id string, r Rect, label string, font FontType, size int) {
	sceneAdd(id, r)
	fbinkFillRect(r, "GRAYD")
	fbinkTextRect(r, label, font, size, "", AlignCenter, true)
}

// drawButtonDisabled draws a button with no touch target (grayed out).
func drawButtonDisabled(r Rect, label string, font FontType, size int) {
	fbinkFillRect(r, "GRAYE")
	fbinkTextRect(r, label, font, size, "GRAYB", AlignCenter, true)
}

// drawLabel draws centered text in a rect (no border, no touch target),
// top-aligned - unchanged from before centerV existed, since none of its
// current callers (title, "Done!" messages) were reported as needing
// vertical centering.
func drawLabel(r Rect, text string, font FontType, size int, color string) {
	fbinkTextRect(r, text, font, size, color, AlignCenter, false)
}

// ============================================================
// Types & globals
// ============================================================

type TouchEvent struct{ X, Y int }

type Screen int

const (
	ScreenDecks Screen = iota
	ScreenFront
	ScreenBack
	ScreenDone
)

type FontType int

const (
	FontMenu FontType = iota
	FontFront
	FontBack
)

const EVIOCGRAB = 0x40044590

var (
	cards       []core.Card
	csvFile     string
	dataDir     = "."
	currentDeck string
	currentCard *core.Card
	decks       []string

	deckPage     int
	decksPerPage int

	touchMaxX = 1440
	touchMaxY = 1020
	screenW   = 1072
	screenH   = 1448

	// screenBPP/currentRota/canRotate are captured from `fbink -e` for
	// future use (e.g. sizing/orienting the canvas correctly on panels
	// other than Clara BW) but are not consumed by anything yet in this
	// pass - touch handling is confirmed independent of these values.
	// Defaults match Clara BW; unused until something reads them.
	screenBPP   = 8
	currentRota = 0
	canRotate   = false

	// screenDPI drives font rasterization size (see FontCache.Face) - the
	// point size in anki-fbink.conf only comes out at the intended
	// physical size if this matches the panel's real density. Confirmed
	// on real hardware: Clara BW reports 300, not the 96 this used to be
	// hardcoded to (a ~3x too-small font bug). Defaults to 300 (matching
	// Clara BW, like the screenW/screenH fallbacks above) rather than a
	// generic desktop 96, so a failed detectScreen() parse doesn't silently
	// reproduce the same bug on the primary target device.
	screenDPI = 300

	// canvas is the whole-screen buffer every draw*Screen composes into
	// before a single flushScreen() call blits it to the e-ink panel.
	// Initialized in main() once the real screen size is known.
	canvas *Canvas

	reverseMode = false

	touchDevice   = "/dev/input/event1"
	fbinkPath     = "fbink"
	touchFd       int // raw syscall fd — bypasses Go runtime poller
	debug         = false
	lastTouchTime time.Time
	touchCooldown = 300 * time.Millisecond

	cfg = Style{
		SizeTitle:  24,
		SizeCard:   28,
		SizeMenu:   16,
		LineHeight: 1.0, // 1.0 = font's natural metrics height, unchanged from before this setting existed
		Margin:     0,   // 0 = unchanged from before this setting existed (content area runs edge to edge)
	}

	// deckOverrides holds per-[deck:NAME] style overrides parsed from
	// anki-fbink.conf, keyed by deck name (the CSV filename without
	// ".csv" - same as currentDeck). Looked up in effectiveStyle().
	deckOverrides = make(map[string]*StyleOverride)

	// activeStyle is the style in effect for the screen currently being
	// drawn - cfg (global defaults) for chrome screens (decks/done), or
	// effectiveStyle(currentDeck) for the front/back card screens, set at
	// the top of each draw*Screen. resolveFont/resolveFontStyle and all
	// per-screen size/margin/line-height lookups read this, not cfg
	// directly, so a [deck:NAME] override actually takes effect.
	activeStyle = cfg
)

// Style is the fully-resolved set of display settings used for rendering -
// either the global defaults (cfg) or the result of merging a deck's
// overrides on top of them (see effectiveStyle).
type Style struct {
	FontDir         string
	FontFront       string
	FontBack        string
	FontMenu        string
	FontFrontBold   string // optional explicit override; "" triggers convention-probe
	FontFrontItalic string
	FontBackBold    string
	FontBackItalic  string
	FontMenuBold    string
	FontMenuItalic  string
	SizeTitle       int
	SizeCard        int
	SizeMenu        int
	LineHeight      float64 // multiplier on the font's natural line height
	Margin          int     // pixels inset on all sides of the content area
	DarkMode        bool
}

// StyleOverride is a [deck:NAME] section: every field is a pointer so a
// nil field means "not set in this section, inherit the global default"
// rather than "explicitly set to zero/empty".
type StyleOverride struct {
	FontDir         *string
	FontFront       *string
	FontBack        *string
	FontMenu        *string
	FontFrontBold   *string
	FontFrontItalic *string
	FontBackBold    *string
	FontBackItalic  *string
	FontMenuBold    *string
	FontMenuItalic  *string
	SizeTitle       *int
	SizeCard        *int
	SizeMenu        *int
	LineHeight      *float64
	Margin          *int
	DarkMode        *bool
}

// effectiveStyle merges any [deck:deckName] override on top of the global
// cfg defaults. Called wherever rendering needs to know "the style for the
// deck currently on screen" (currentDeck).
func effectiveStyle(deckName string) Style {
	o, ok := deckOverrides[deckName]
	if !ok {
		return cfg
	}
	return applyStyleOverride(cfg, o)
}

// applyStyleOverride returns s with every field the override actually sets
// replaced. Used both to merge a [deck:NAME] section onto the global
// defaults and, in loadConfig, to fold the pre-section keys into those
// defaults - so the config file's keys are recognised by exactly one switch
// (applyStyleKey) instead of two parallel ones that had to be kept in sync.
func applyStyleOverride(s Style, o *StyleOverride) Style {
	if o.FontDir != nil {
		s.FontDir = *o.FontDir
	}
	if o.FontFront != nil {
		s.FontFront = *o.FontFront
	}
	if o.FontBack != nil {
		s.FontBack = *o.FontBack
	}
	if o.FontMenu != nil {
		s.FontMenu = *o.FontMenu
	}
	if o.FontFrontBold != nil {
		s.FontFrontBold = *o.FontFrontBold
	}
	if o.FontFrontItalic != nil {
		s.FontFrontItalic = *o.FontFrontItalic
	}
	if o.FontBackBold != nil {
		s.FontBackBold = *o.FontBackBold
	}
	if o.FontBackItalic != nil {
		s.FontBackItalic = *o.FontBackItalic
	}
	if o.FontMenuBold != nil {
		s.FontMenuBold = *o.FontMenuBold
	}
	if o.FontMenuItalic != nil {
		s.FontMenuItalic = *o.FontMenuItalic
	}
	if o.SizeTitle != nil {
		s.SizeTitle = *o.SizeTitle
	}
	if o.SizeCard != nil {
		s.SizeCard = *o.SizeCard
	}
	if o.SizeMenu != nil {
		s.SizeMenu = *o.SizeMenu
	}
	if o.LineHeight != nil {
		s.LineHeight = *o.LineHeight
	}
	if o.Margin != nil {
		s.Margin = *o.Margin
	}
	if o.DarkMode != nil {
		s.DarkMode = *o.DarkMode
	}
	return s
}

// Base layout regions (recomputed after screen detection)
var (
	navRect     Rect
	contentRect Rect
	actionRect  Rect
)

func computeLayout() {
	navRect = rectPct(0, 0, 100, 8)
	contentRect = rectPct(0, 8, 100, 70)
	actionRect = rectPct(0, 78, 100, 22)
}

// ============================================================
// Config
// ============================================================

// loadConfig reads anki-fbink.conf. Keys before any [deck:NAME] section set
// the global defaults (cfg); keys inside a [deck:NAME] section become that
// deck's override, applied on top of the defaults by effectiveStyle. This
// mirrors anki-mywords.conf's existing [nl]/[de]/[fr] section convention.
func loadConfig() {
	paths := []string{"./anki-fbink.conf", filepath.Join(dataDir, "anki-fbink.conf")}
	var data []byte
	var confPath string
	for _, p := range paths {
		var err error
		if data, err = os.ReadFile(p); err == nil {
			confPath = p
			break
		}
	}
	if data == nil {
		return
	}

	warn := core.NewConfigWarnings(confPath)
	defer warn.Summary()

	// Keys before the first [deck:NAME] section are parsed into an override
	// too, then folded into cfg below - so applyStyleKey is the single place
	// that knows the config file's key names.
	globalOverride := &StyleOverride{}
	override := globalOverride

	for i, line := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}

		if strings.HasPrefix(line, "[deck:") && strings.HasSuffix(line, "]") {
			deckName := strings.TrimSuffix(strings.TrimPrefix(line, "[deck:"), "]")
			override = &StyleOverride{}
			deckOverrides[deckName] = override
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])

		// touch_cooldown is the one setting that is not part of Style, and
		// is global-only - a [deck:NAME] section has no use for it.
		if key == "touch_cooldown" {
			if override != globalOverride {
				warn.UnknownKey(lineNo, key)
			} else if v, ok := warn.Int(lineNo, key, value); ok {
				touchCooldown = time.Duration(v) * time.Millisecond
			}
			continue
		}

		applyStyleKey(override, key, value, lineNo, warn)
	}

	cfg = applyStyleOverride(cfg, globalOverride)

	if debug {
		fmt.Printf("Config: FontDir=%s, DarkMode=%v, deckOverrides=%d\n", cfg.FontDir, cfg.DarkMode, len(deckOverrides))
	}
}

// applyStyleKey sets one key=value pair on a [deck:NAME] override. Only
// display-relevant keys are recognized here (touch_cooldown etc. stay
// global-only) - unrecognized keys are silently ignored, same as the
// global switch above.
func applyStyleKey(o *StyleOverride, key, value string, lineNo int, warn *core.ConfigWarnings) {
	switch key {
	case "font_dir":
		o.FontDir = &value
	case "font_front":
		o.FontFront = &value
	case "font_back":
		o.FontBack = &value
	case "font_menu":
		o.FontMenu = &value
	case "font_front_bold":
		o.FontFrontBold = &value
	case "font_front_italic":
		o.FontFrontItalic = &value
	case "font_back_bold":
		o.FontBackBold = &value
	case "font_back_italic":
		o.FontBackItalic = &value
	case "font_menu_bold":
		o.FontMenuBold = &value
	case "font_menu_italic":
		o.FontMenuItalic = &value
	case "size_title":
		if v, ok := warn.Int(lineNo, key, value); ok {
			o.SizeTitle = &v
		}
	case "size_card":
		if v, ok := warn.Int(lineNo, key, value); ok {
			o.SizeCard = &v
		}
	case "size_menu":
		if v, ok := warn.Int(lineNo, key, value); ok {
			o.SizeMenu = &v
		}
	case "line_height":
		if v, ok := warn.Float(lineNo, key, value); ok {
			o.LineHeight = &v
		}
	case "margin":
		if v, ok := warn.Int(lineNo, key, value); ok {
			o.Margin = &v
		}
	case "darkmode":
		if v, ok := warn.Bool(lineNo, key, value); ok {
			o.DarkMode = &v
		}
	default:
		warn.UnknownKey(lineNo, key)
	}
}

// resolveFont and resolveFontStyle read activeStyle (not cfg directly) so
// a [deck:NAME] override set via activeStyle at the top of a draw*Screen
// function takes effect.
func resolveFont(ft FontType) string {
	if activeStyle.FontDir == "" {
		return ""
	}
	var name string
	switch ft {
	case FontFront:
		name = activeStyle.FontFront
	case FontBack:
		name = activeStyle.FontBack
	case FontMenu:
		name = activeStyle.FontMenu
	}
	if name == "" {
		return ""
	}
	return filepath.Join(activeStyle.FontDir, name)
}

// resolveFontStyle finds the best available font file for (ft, style):
// an explicit config override, then a same-directory file following the
// "Regular"->"Bold"/"Italic"/"BoldItalic" naming convention, then the
// regular file itself. synthBold reports whether the caller should apply
// a synthetic double-strike bold (true only when Bold/BoldItalic was
// requested but no real bold file exists anywhere). There is no synthetic
// italic: without a real italic/oblique file, italic silently renders as
// regular - shearing rasterized glyphs by hand was judged too likely to
// produce corrupted output to risk without on-device verification.
func resolveFontStyle(ft FontType, style FontStyle) (path string, synthBold bool) {
	regular := resolveFont(ft)
	if regular == "" || style == StyleRegular {
		return regular, false
	}

	explicit, convention := "", ""
	switch ft {
	case FontFront:
		if style == StyleBold || style == StyleBoldItalic {
			explicit = activeStyle.FontFrontBold
		} else {
			explicit = activeStyle.FontFrontItalic
		}
	case FontBack:
		if style == StyleBold || style == StyleBoldItalic {
			explicit = activeStyle.FontBackBold
		} else {
			explicit = activeStyle.FontBackItalic
		}
	case FontMenu:
		if style == StyleBold || style == StyleBoldItalic {
			explicit = activeStyle.FontMenuBold
		} else {
			explicit = activeStyle.FontMenuItalic
		}
	}
	if explicit != "" {
		return filepath.Join(activeStyle.FontDir, explicit), false
	}

	convention = probeFontVariant(regular, style)
	if convention != "" {
		return convention, false
	}

	// No real bold/italic file: synthesize bold, accept plain for italic.
	return regular, style == StyleBold || style == StyleBoldItalic
}

// probeFontVariant looks for a sibling font file following the common
// "Foo-Regular.ttf" -> "Foo-Bold.ttf"/"Foo-Italic.ttf"/"Foo-BoldItalic.ttf"
// naming convention. Returns "" if no such file exists.
func probeFontVariant(regularPath string, style FontStyle) string {
	dir := filepath.Dir(regularPath)
	base := filepath.Base(regularPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	var suffix string
	switch style {
	case StyleBold:
		suffix = "Bold"
	case StyleItalic:
		suffix = "Italic"
	case StyleBoldItalic:
		suffix = "BoldItalic"
	default:
		return ""
	}

	var candidateStem string
	if strings.HasSuffix(stem, "Regular") {
		candidateStem = strings.TrimSuffix(stem, "Regular") + suffix
	} else {
		candidateStem = stem + "-" + suffix
	}

	candidate := filepath.Join(dir, candidateStem+ext)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func findFbink() string {
	for _, p := range []string{
		"./bin/fbink",
		"/mnt/onboard/.adds/nm/fbink",
		"/mnt/onboard/.adds/koreader/fbink",
		"/usr/local/Kobo/fbink",
		"/usr/local/bin/fbink",
		"/usr/bin/fbink",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "fbink"
}

// detectScreen parses `fbink -e`'s output for screen geometry. Confirmed
// against real device output (Kobo Clara BW): it's a single line of
// semicolon-separated key=value pairs, shell-eval style (values are
// sometimes single-quoted strings), e.g.:
//
//	viewWidth=1072;viewHeight=1448;...;DPI=300;BPP=32;...;currentRota=3;canRotate=0;...
//
// Not newline-separated "key: value" pairs, which an earlier version of
// this parser incorrectly assumed - that version silently matched nothing
// at all and got away with it undetected only because the hardcoded
// screenW/screenH fallback values happened to already be correct for
// Clara BW. Field names are also case-sensitive and don't all match the
// FBInkState C struct's capitalization (it's "DPI" not "ScreenDPI",
// "currentRota"/"canRotate" not "CurrentRota"/"CanRotate").
func detectScreen() {
	out, err := exec.Command(fbinkPath, "-e").Output()
	if err != nil {
		return
	}

	fields := make(map[string]string)
	for _, kv := range strings.Split(strings.TrimSpace(string(out)), ";") {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			continue
		}
		fields[strings.TrimSpace(parts[0])] = strings.Trim(strings.TrimSpace(parts[1]), "'")
	}

	if v, ok := fields["viewWidth"]; ok {
		if w, err := strconv.Atoi(v); err == nil {
			screenW = w
		}
	}
	if v, ok := fields["viewHeight"]; ok {
		if h, err := strconv.Atoi(v); err == nil {
			screenH = h
		}
	}
	if v, ok := fields["BPP"]; ok {
		if b, err := strconv.Atoi(v); err == nil {
			screenBPP = b
		}
	}
	if v, ok := fields["currentRota"]; ok {
		if r, err := strconv.Atoi(v); err == nil {
			currentRota = r
		}
	}
	if v, ok := fields["canRotate"]; ok {
		canRotate = v == "1" || v == "true"
	}
	if v, ok := fields["DPI"]; ok {
		if d, err := strconv.Atoi(v); err == nil && d > 0 {
			screenDPI = d
		}
	}

	if debug {
		fmt.Printf("Detected screen: %dx%d bpp=%d rota=%d canRotate=%v dpi=%d\n",
			screenW, screenH, screenBPP, currentRota, canRotate, screenDPI)
	}
}

// ============================================================
// Touch input
// ============================================================

func findTouchDevice() string {
	data, err := os.ReadFile("/proc/bus/input/devices")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		inBlock := false
		for _, line := range lines {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "touch") || strings.Contains(lower, "cyttsp") ||
				strings.Contains(lower, "elan") || strings.Contains(lower, "ft5") ||
				strings.Contains(lower, "wacom") {
				inBlock = true
			}
			if inBlock && strings.HasPrefix(line, "H: Handlers=") {
				for _, p := range strings.Fields(line) {
					if strings.HasPrefix(p, "event") {
						return "/dev/input/" + p
					}
				}
			}
			if line == "" {
				inBlock = false
			}
		}
	}
	for _, d := range []string{"/dev/input/event1", "/dev/input/event0", "/dev/input/event2", "/dev/input/event3"} {
		if _, err := os.Stat(d); err == nil {
			return d
		}
	}
	return "/dev/input/event1"
}

func grabTouchDevice() error {
	touchDevice = findTouchDevice()

	fd, err := syscall.Open(touchDevice, syscall.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open touch device: %v", err)
	}
	touchFd = fd

	one := 1
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(touchFd), EVIOCGRAB, uintptr(unsafe.Pointer(&one)))
	if errno != 0 {
		syscall.Close(touchFd)
		return fmt.Errorf("failed to grab touch device: %v", errno)
	}
	return nil
}

func releaseTouchDevice() {
	if touchFd > 0 {
		zero := 0
		syscall.Syscall(syscall.SYS_IOCTL, uintptr(touchFd), EVIOCGRAB, uintptr(unsafe.Pointer(&zero)))
		syscall.Close(touchFd)
	}
}

// Clara BW with rotation 3: axes are swapped
func transformTouch(rawX, rawY int) (int, int) {
	return (touchMaxY - rawY) * screenW / touchMaxY, rawX * screenH / touchMaxX
}

func drainTouch() {
	if touchFd <= 0 {
		return
	}
	syscall.SetNonblock(touchFd, true)
	buf := make([]byte, 16)
	for {
		_, err := syscall.Read(touchFd, buf)
		if err != nil {
			break
		}
	}
	syscall.SetNonblock(touchFd, false)
	lastTouchTime = time.Now()
}

func readTouch() (TouchEvent, bool) {
	if touchFd <= 0 {
		return TouchEvent{}, false
	}

	var x, y int
	var hasX, hasY bool

	buf := make([]byte, 16)
	for {
		n, err := syscall.Read(touchFd, buf)
		if err != nil || n < 16 {
			return TouchEvent{}, false
		}

		typ := binary.LittleEndian.Uint16(buf[8:10])
		code := binary.LittleEndian.Uint16(buf[10:12])
		value := int32(binary.LittleEndian.Uint32(buf[12:16]))

		if typ == 3 { // EV_ABS
			if code == 0 || code == 53 { // ABS_X or ABS_MT_POSITION_X
				x = int(value)
				hasX = true
			} else if code == 1 || code == 54 { // ABS_Y or ABS_MT_POSITION_Y
				y = int(value)
				hasY = true
			}
		}

		if typ == 0 && code == 0 && hasX && hasY { // SYN_REPORT
			if debug {
				fmt.Printf("Raw: x=%d y=%d\n", x, y)
			}
			tx, ty := transformTouch(x, y)
			return TouchEvent{X: tx, Y: ty}, true
		}
	}
}

// ============================================================
// Card helpers
// ============================================================

func randomDueCard() *core.Card {
	return core.RandomDueCard(cards)
}

func displayFront() string {
	if reverseMode {
		return currentCard.Back
	}
	return currentCard.Front
}

func displayBack() string {
	if reverseMode {
		return currentCard.Front
	}
	return currentCard.Back
}

// ============================================================
// Screen drawing
// ============================================================

func drawDecksScreen() {
	activeStyle = cfg // chrome screen, no single deck in context
	sceneClear()
	canvas.Clear(0xFF)

	// Title (with top margin)
	topMargin := screenH * 5 / 100
	titleRect := Rect{navRect.X, topMargin, navRect.W, navRect.H + screenH*8/100}
	drawLabel(titleRect, "Kobo Anki", FontMenu, activeStyle.SizeTitle, "")

	// Deck list area: below title, above action
	deckAreaTop := titleRect.Y + titleRect.H
	deckAreaH := actionRect.Y - deckAreaTop
	deckRowH := screenH * 7 / 100
	if deckRowH > 0 {
		decksPerPage = deckAreaH / deckRowH
	}
	if decksPerPage < 1 {
		decksPerPage = 1
	}

	decks = core.ListDecks(dataDir)
	totalPages := (len(decks) + decksPerPage - 1) / decksPerPage
	if totalPages < 1 {
		totalPages = 1
	}
	if deckPage >= totalPages {
		deckPage = totalPages - 1
	}
	if deckPage < 0 {
		deckPage = 0
	}

	start := deckPage * decksPerPage
	end := start + decksPerPage
	if end > len(decks) {
		end = len(decks)
	}

	for i, d := range decks[start:end] {
		r := Rect{contentRect.X, deckAreaTop + i*deckRowH, contentRect.W, deckRowH}
		id := fmt.Sprintf("deck-%d", start+i)
		sceneAdd(id, r)

		path := core.DeckCSVPath(dataDir, d)
		c, _ := core.LoadCards(path)
		due := core.CountDueCards(c)

		// Deck name on the left, due count in gray on the right
		nameRect := Rect{r.X + screenW/20, r.Y, r.W / 2, r.H}
		fbinkTextRect(nameRect, d, FontMenu, activeStyle.SizeMenu*3/4, "", AlignLeft, true)

		dueRect := Rect{r.X, r.Y, r.W - screenW/20, r.H}
		dueText := fmt.Sprintf("%d due", due)
		fbinkTextRect(dueRect, dueText, FontMenu, activeStyle.SizeMenu*3/4, "GRAY8", AlignRight, true)
	}

	// Action zone: 2x2 grid — prev/next on top row, reverse/exit on bottom row
	gap := screenW / 30
	actionInner := inset(actionRect, gap/2)
	rows := splitV(actionInner, 2, gap)
	topCols := splitH(rows[0], 2, gap)
	botCols := splitH(rows[1], 2, gap)

	// Always show prev/next buttons; grayed out when not actionable
	if deckPage > 0 {
		drawButton("prev", topCols[0], "< Prev", FontMenu, activeStyle.SizeMenu/2)
	} else {
		drawButtonDisabled(topCols[0], "< Prev", FontMenu, activeStyle.SizeMenu/2)
	}
	if deckPage < totalPages-1 {
		drawButton("next", topCols[1], "Next >", FontMenu, activeStyle.SizeMenu/2)
	} else {
		drawButtonDisabled(topCols[1], "Next >", FontMenu, activeStyle.SizeMenu/2)
	}
	// if totalPages > 1 {
	// 	indicator := fmt.Sprintf("page %d/%d", deckPage+1, totalPages)
	// 	drawLabel(rows[0], indicator, FontMenu, activeStyle.SizeMenu/2, "GRAY8")
	// }

	reverseLabel := "Reverse"
	if reverseMode {
		reverseLabel = "Normal"
	}
	drawButton("reverse", botCols[0], reverseLabel, FontMenu, activeStyle.SizeMenu/2)
	drawButton("exit", botCols[1], "Quit", FontMenu, activeStyle.SizeMenu/2)

	flushScreen()
	drainTouch()
}

func drawFrontScreen() {
	activeStyle = effectiveStyle(currentDeck)
	sceneClear()
	// Fill screen without refresh (avoids flash between back→front)
	fbinkFillRect(Rect{0, 0, screenW, screenH}, "WHITE")

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4 // half of a rating button row
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, activeStyle.SizeMenu/2)

	// Card content area: whatever space actually remains between the back
	// button and the action zone, not the static contentRect - a fixed
	// screen percentage can't account for backRect's real height or for
	// text now rendering at its correct (larger, DPI-accurate) physical
	// size, and previously let long content overlap the action zone.
	availTop := backRect.Y + backRect.H + gap
	availRect := Rect{contentRect.X, availTop, contentRect.W, actionRect.Y - availTop}
	cardRect := inset(availRect, activeStyle.Margin)
	drawMarkdownLabel(cardRect, displayFront(), FontFront, activeStyle.SizeCard, "", AlignCenter, true)

	// Any tap on content or action area shows answer
	sceneAdd("show", contentRect)
	sceneAdd("show", actionRect)

	flushScreen()
	drainTouch()
}

func drawBackScreen() {
	activeStyle = effectiveStyle(currentDeck)
	sceneClear()
	// Fill screen without refresh (avoids flash between front→back)
	fbinkFillRect(Rect{0, 0, screenW, screenH}, "WHITE")

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, activeStyle.SizeMenu/2)

	// See drawFrontScreen: real remaining space, not a static contentRect.
	availTop := backRect.Y + backRect.H + gap
	availRect := Rect{contentRect.X, availTop, contentRect.W, actionRect.Y - availTop}
	cardRect := inset(availRect, activeStyle.Margin)

	// Front text (small, gray, above the answer) - just the headword
	// (headwordOnly), not the full front face: with dict_examples/
	// card_format, the front can carry alternatives and an example too,
	// which is too much to restate here - this is a reminder of which word
	// the card is about, not a second copy of the front screen. Fixed
	// height (2 lines' worth at SizeMenu, from font metrics alone)
	// regardless of the actual front/answer content, so its position never
	// shifts card to card - content is then centered within that stable
	// slot.
	frontLineHeight := activeStyle.SizeMenu * 2 // conservative fallback if font resolution fails
	if regular, err := makeFaceResolver(FontFront, activeStyle.SizeMenu)(StyleRegular); err == nil {
		frontLineHeight = scaledLineHeight(regular.Face)
	}
	frontRect := Rect{cardRect.X, cardRect.Y, cardRect.W, frontLineHeight * 2}
	drawMarkdownLabel(frontRect, core.HeadwordOnly(displayFront()), FontFront, activeStyle.SizeMenu, "GRAY8", AlignCenter, true)

	// Answer text — this rect is itself fixed (anchored right below the
	// fixed frontRect), so centering within it can't cause any jumping;
	// only the answer's own content shifts its centering, never the front
	// label's position.
	answerTop := frontRect.Y + frontRect.H + gap
	answerRect := Rect{cardRect.X, answerTop, cardRect.W, cardRect.Y + cardRect.H - answerTop}
	drawMarkdownLabel(answerRect, displayBack(), FontBack, activeStyle.SizeCard, "", AlignCenter, true)

	// Rating buttons: 2x2 grid in action zone
	actionInner := inset(actionRect, gap/2)
	rows := splitV(actionInner, 2, gap)
	topCols := splitH(rows[0], 2, gap)
	botCols := splitH(rows[1], 2, gap)

	drawButton("hard", topCols[0], "Hard", FontMenu, activeStyle.SizeMenu/2)
	drawButton("good", topCols[1], "Good", FontMenu, activeStyle.SizeMenu/2)
	drawButton("again", botCols[0], "Again", FontMenu, activeStyle.SizeMenu/2)
	drawButton("easy", botCols[1], "Easy", FontMenu, activeStyle.SizeMenu/2)

	flushScreen()
	drainTouch()
}

func drawDoneScreen() {
	activeStyle = cfg // chrome screen
	sceneClear()
	canvas.Clear(0xFF)

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, activeStyle.SizeMenu/2)

	// "Done!" centered
	topHalf := Rect{contentRect.X, contentRect.Y, contentRect.W, contentRect.H / 2}
	drawLabel(topHalf, "Done!", FontMenu, activeStyle.SizeCard, "")

	botHalf := Rect{contentRect.X, contentRect.Y + contentRect.H/2, contentRect.W, contentRect.H / 2}
	drawLabel(botHalf, fmt.Sprintf("No more cards due in %s", currentDeck), FontMenu, activeStyle.SizeMenu, "")

	// Any touch goes back to decks
	sceneAdd("any", contentRect)
	sceneAdd("any", actionRect)

	flushScreen()
	drainTouch()
}

// ============================================================
// Main loop
// ============================================================

func rateAndAdvance(rating fsrs.Rating) Screen {
	card := core.FindCard(cards, currentCard.Key())
	if card != nil {
		core.Review(card, rating)
		core.SaveCards(csvFile, cards)
	}
	currentCard = randomDueCard()
	if currentCard == nil {
		drawDoneScreen()
		return ScreenDone
	}
	drawFrontScreen()
	return ScreenFront
}

func main() {
	fbinkPath = findFbink()
	debug = os.Getenv("DEBUG") == "1"

	coreCfg := core.LoadCoreConfig("anki-core.conf")
	dataDir = coreCfg.DataDir
	reverseMode = coreCfg.Reverse
	core.InitScheduler(coreCfg.RequestRetention, coreCfg.MaximumInterval, coreCfg.EnableShortTerm)

	loadConfig()
	detectScreen()
	computeLayout()
	canvas = NewCanvas(screenW, screenH)

	// CLI arg overrides config
	if len(os.Args) > 1 {
		dataDir = os.Args[1]
	}

	if err := grabTouchDevice(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not grab touch device: %v\n", err)
	}
	defer releaseTouchDevice()

	screen := ScreenDecks
	drawDecksScreen()

	for {
		ev, ok := readTouch()
		if !ok {
			continue
		}

		now := time.Now()
		if now.Sub(lastTouchTime) < touchCooldown {
			if debug {
				fmt.Printf("Touch ignored (cooldown)\n")
			}
			continue
		}
		lastTouchTime = now

		id := sceneHitTest(ev.X, ev.Y)
		if debug {
			fmt.Printf("Touch: x=%d y=%d id=%q screen=%d\n", ev.X, ev.Y, id, screen)
		}

		switch screen {
		case ScreenDecks:
			switch {
			case id == "exit":
				canvas.Clear(0xFF)
				flushScreen()
				return
			case id == "reverse":
				reverseMode = !reverseMode
				drawDecksScreen()
			case id == "prev" && deckPage > 0:
				deckPage--
				drawDecksScreen()
			case id == "next":
				deckPage++
				drawDecksScreen()
			case strings.HasPrefix(id, "deck-"):
				idx, _ := strconv.Atoi(strings.TrimPrefix(id, "deck-"))
				if idx >= 0 && idx < len(decks) {
					currentDeck = decks[idx]
					csvFile = core.DeckCSVPath(dataDir, currentDeck)
					cards, _ = core.LoadCards(csvFile)
					currentCard = randomDueCard()
					if currentCard == nil {
						screen = ScreenDone
						drawDoneScreen()
					} else {
						screen = ScreenFront
						drawFrontScreen()
					}
				}
			}

		case ScreenFront:
			if id == "back" {
				screen = ScreenDecks
				drawDecksScreen()
			} else if id == "show" {
				screen = ScreenBack
				drawBackScreen()
			}

		case ScreenBack:
			switch id {
			case "back":
				screen = ScreenDecks
				drawDecksScreen()
			case "again":
				screen = rateAndAdvance(fsrs.Again)
			case "hard":
				screen = rateAndAdvance(fsrs.Hard)
			case "good":
				screen = rateAndAdvance(fsrs.Good)
			case "easy":
				screen = rateAndAdvance(fsrs.Easy)
			}

		case ScreenDone:
			if id != "" {
				screen = ScreenDecks
				drawDecksScreen()
			}
		}
	}
}
