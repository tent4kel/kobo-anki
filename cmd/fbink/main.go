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
	AlignLeft   Align = iota
	AlignCenter
	AlignRight
)

// inset shrinks a rect by d pixels on each side.
func inset(r Rect, d int) Rect {
	return Rect{r.X + d, r.Y + d, r.W - 2*d, r.H - 2*d}
}

// vcenter returns a copy of r with top adjusted to vertically center text
// of the given point size. Approximation: rendered height ~ size * 2 pixels.
func vcenter(r Rect, size int) Rect {
	textH := size * 2
	offset := (r.H - textH) / 2
	if offset < 0 {
		offset = 0
	}
	return Rect{r.X, r.Y + offset, r.W, r.H - offset}
}

// ============================================================
// FBInk primitives
// ============================================================

// fbinkFillRect fills a rectangle with a color (no screen refresh).
func fbinkFillRect(r Rect, color string) {
	region := fmt.Sprintf("top=%d,left=%d,width=%d,height=%d", r.Y, r.X, r.W, r.H)
	args := []string{"-k", region, "-b"}
	if color != "" {
		args = append(args, "-B", color)
	}
	if cfg.DarkMode {
		args = append(args, "-H")
	}
	exec.Command(fbinkPath, args...).Run()
}

// fbinkTextRect draws text inside a rect (bgless overlay, no refresh).
func fbinkTextRect(r Rect, text string, font FontType, size int, color string, align Align) {
	fontPath := resolveFont(font)
	if fontPath == "" {
		// Fallback to bitmap font
		row := r.Y * 20 / screenH
		args := []string{"-y", strconv.Itoa(row), "-S", strconv.Itoa(size / 8), "-O", "-b"}
		if align == AlignCenter {
			args = append(args, "-m")
		}
		if color != "" {
			args = append(args, "-C", color)
		}
		if cfg.DarkMode {
			args = append(args, "-H")
		}
		args = append(args, text)
		if debug {
			fmt.Printf("fbink: %v\n", args)
		}
		cmd := exec.Command(fbinkPath, args...)
		out, err := cmd.CombinedOutput()
		if err != nil && debug {
			fmt.Printf("fbink error: %v, output: %s\n", err, string(out))
		}
		return
	}

	left := r.X
	right := screenW - (r.X + r.W)
	if right < 0 {
		right = 0
	}

	// For right-align: push left margin so text hugs the right edge
	if align == AlignRight {
		left = r.X + r.W*2/3
	}

	tt := fmt.Sprintf("regular=%s,size=%d,top=%d,left=%d,right=%d",
		fontPath, size, r.Y, left, right)

	args := []string{"-t", tt, "-O", "-b"}
	if align == AlignCenter {
		args = append(args, "-m")
	}
	if color != "" {
		args = append(args, "-C", color)
	}
	if cfg.DarkMode {
		args = append(args, "-H")
	}
	args = append(args, text)

	if debug {
		fmt.Printf("fbink: %v\n", args)
	}
	cmd := exec.Command(fbinkPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && debug {
		fmt.Printf("fbink error: %v, output: %s\n", err, string(out))
	}
}

// drawButton draws a filled button and registers it as a touch target.
func drawButton(id string, r Rect, label string, font FontType, size int) {
	sceneAdd(id, r)
	fbinkFillRect(r, "GRAYD")
	fbinkTextRect(vcenter(r, size), label, font, size, "", AlignCenter)
}

// drawButtonDisabled draws a button with no touch target (grayed out).
func drawButtonDisabled(r Rect, label string, font FontType, size int) {
	fbinkFillRect(r, "GRAYE")
	fbinkTextRect(vcenter(r, size), label, font, size, "GRAYB", AlignCenter)
}

// drawLabel draws centered text in a rect (no border, no touch target).
func drawLabel(r Rect, text string, font FontType, size int, color string) {
	fbinkTextRect(r, text, font, size, color, AlignCenter)
}

func fbinkClear() {
	args := []string{"-c"}
	if cfg.DarkMode {
		args = append(args, "-H")
	}
	exec.Command(fbinkPath, args...).Run()
}

func fbinkRefresh() {
	exec.Command(fbinkPath, "-s").Run()
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

	reverseMode = false

	touchDevice   = "/dev/input/event1"
	fbinkPath     = "fbink"
	touchFd       int // raw syscall fd — bypasses Go runtime poller
	debug         = false
	lastTouchTime time.Time
	touchCooldown = 300 * time.Millisecond

	cfg = struct {
		FontDir   string
		FontFront string
		FontBack  string
		FontMenu  string
		SizeTitle int
		SizeCard  int
		SizeMenu  int
		DarkMode  bool
	}{
		SizeTitle: 24,
		SizeCard:  28,
		SizeMenu:  16,
	}
)

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

func loadConfig() {
	paths := []string{"./anki-fbink.conf", filepath.Join(dataDir, "anki-fbink.conf")}
	var data []byte
	for _, p := range paths {
		var err error
		if data, err = os.ReadFile(p); err == nil {
			break
		}
	}
	if data == nil {
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch key {
		case "font_dir":
			cfg.FontDir = value
		case "font_front":
			cfg.FontFront = value
		case "font_back":
			cfg.FontBack = value
		case "font_menu":
			cfg.FontMenu = value
		case "size_title":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.SizeTitle = v
			}
		case "size_card":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.SizeCard = v
			}
		case "size_menu":
			if v, err := strconv.Atoi(value); err == nil {
				cfg.SizeMenu = v
			}
		case "darkmode":
			cfg.DarkMode = value == "true" || value == "1"
		case "touch_cooldown":
			if v, err := strconv.Atoi(value); err == nil {
				touchCooldown = time.Duration(v) * time.Millisecond
			}
		}
	}

	if debug {
		fmt.Printf("Config: FontDir=%s, DarkMode=%v\n", cfg.FontDir, cfg.DarkMode)
	}
}

func resolveFont(ft FontType) string {
	if cfg.FontDir == "" {
		return ""
	}
	var name string
	switch ft {
	case FontFront:
		name = cfg.FontFront
	case FontBack:
		name = cfg.FontBack
	case FontMenu:
		name = cfg.FontMenu
	}
	if name == "" {
		return ""
	}
	return filepath.Join(cfg.FontDir, name)
}

func findFbink() string {
	for _, p := range []string{
		"./bin/fbink",
		"/mnt/onboard/.adds/nm/fbink",
		"/usr/local/bin/fbink",
		"/usr/bin/fbink",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "fbink"
}

func detectScreen() {
	out, err := exec.Command(fbinkPath, "-e").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "viewWidth") {
			if parts := strings.Split(line, ":"); len(parts) >= 2 {
				if w, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					screenW = w
				}
			}
		}
		if strings.Contains(line, "viewHeight") {
			if parts := strings.Split(line, ":"); len(parts) >= 2 {
				if h, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					screenH = h
				}
			}
		}
	}
	if debug {
		fmt.Printf("Detected screen: %dx%d\n", screenW, screenH)
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
	sceneClear()
	fbinkClear()

	// Title (with top margin)
	topMargin := screenH * 5 / 100
	titleRect := Rect{navRect.X, topMargin, navRect.W, navRect.H + screenH*8/100}
	drawLabel(titleRect, "Kobo Anki", FontMenu, cfg.SizeTitle, "")

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
		nameRect := Rect{r.X + screenW/20, r.Y, r.W/2, r.H}
		fbinkTextRect(vcenter(nameRect, cfg.SizeMenu*3/4), d, FontMenu, cfg.SizeMenu*3/4, "", AlignLeft)

		dueRect := Rect{r.X, r.Y, r.W - screenW/20, r.H}
		dueText := fmt.Sprintf("%d due", due)
		fbinkTextRect(vcenter(dueRect, cfg.SizeMenu*3/4), dueText, FontMenu, cfg.SizeMenu*3/4, "GRAY8", AlignRight)
	}

	// Action zone: 2x2 grid — prev/next on top row, reverse/exit on bottom row
	gap := screenW / 30
	actionInner := inset(actionRect, gap/2)
	rows := splitV(actionInner, 2, gap)
	topCols := splitH(rows[0], 2, gap)
	botCols := splitH(rows[1], 2, gap)

	// Always show prev/next buttons; grayed out when not actionable
	if deckPage > 0 {
		drawButton("prev", topCols[0], "< Prev", FontMenu, cfg.SizeMenu/2)
	} else {
		drawButtonDisabled(topCols[0], "< Prev", FontMenu, cfg.SizeMenu/2)
	}
	if deckPage < totalPages-1 {
		drawButton("next", topCols[1], "Next >", FontMenu, cfg.SizeMenu/2)
	} else {
		drawButtonDisabled(topCols[1], "Next >", FontMenu, cfg.SizeMenu/2)
	}
	// if totalPages > 1 {
	// 	indicator := fmt.Sprintf("page %d/%d", deckPage+1, totalPages)
	// 	drawLabel(rows[0], indicator, FontMenu, cfg.SizeMenu/2, "GRAY8")
	// }

	reverseLabel := "Reverse"
	if reverseMode {
		reverseLabel = "Normal"
	}
	drawButton("reverse", botCols[0], reverseLabel, FontMenu, cfg.SizeMenu/2)
	drawButton("exit", botCols[1], "Quit", FontMenu, cfg.SizeMenu/2)

	fbinkRefresh()
	drainTouch()
}

func drawFrontScreen() {
	sceneClear()
	// Fill screen without refresh (avoids flash between back→front)
	fbinkFillRect(Rect{0, 0, screenW, screenH}, "WHITE")

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4 // half of a rating button row
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, cfg.SizeMenu/2)

	// Card front text — centered in content area (matches answer position on back)
	drawLabel(vcenter(contentRect, cfg.SizeCard), displayFront(), FontFront, cfg.SizeCard, "")

	// Any tap on content or action area shows answer
	sceneAdd("show", contentRect)
	sceneAdd("show", actionRect)

	fbinkRefresh()
	drainTouch()
}

func drawBackScreen() {
	sceneClear()
	// Fill screen without refresh (avoids flash between front→back)
	fbinkFillRect(Rect{0, 0, screenW, screenH}, "WHITE")

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, cfg.SizeMenu/2)

	// Front text (small, gray, below back button with margin)
	frontTop := backRect.Y + backRect.H + gap
	frontRect := Rect{contentRect.X, frontTop, contentRect.W, contentRect.H/3 - gap}
	drawLabel(frontRect, displayFront(), FontFront, cfg.SizeMenu, "GRAY8")

	// Answer text — centered in content area (matches front position)
	drawLabel(vcenter(contentRect, cfg.SizeCard), displayBack(), FontBack, cfg.SizeCard, "")

	// Rating buttons: 2x2 grid in action zone
	actionInner := inset(actionRect, gap/2)
	rows := splitV(actionInner, 2, gap)
	topCols := splitH(rows[0], 2, gap)
	botCols := splitH(rows[1], 2, gap)

	drawButton("hard", topCols[0], "Hard", FontMenu, cfg.SizeMenu/2)
	drawButton("good", topCols[1], "Good", FontMenu, cfg.SizeMenu/2)
	drawButton("again", botCols[0], "Again", FontMenu, cfg.SizeMenu/2)
	drawButton("easy", botCols[1], "Easy", FontMenu, cfg.SizeMenu/2)

	fbinkRefresh()
	drainTouch()
}

func drawDoneScreen() {
	sceneClear()
	fbinkClear()

	// Back button: full width, half the height of a rating button
	gap := screenW / 30
	btnH := (actionRect.H - 2*gap) / 4
	backRect := Rect{gap / 2, gap / 2, screenW - gap, btnH}
	drawButton("back", backRect, "Back", FontMenu, cfg.SizeMenu/2)

	// "Done!" centered
	topHalf := Rect{contentRect.X, contentRect.Y, contentRect.W, contentRect.H / 2}
	drawLabel(topHalf, "Done!", FontMenu, cfg.SizeCard, "")

	botHalf := Rect{contentRect.X, contentRect.Y + contentRect.H/2, contentRect.W, contentRect.H / 2}
	drawLabel(botHalf, fmt.Sprintf("No more cards due in %s", currentDeck), FontMenu, cfg.SizeMenu, "")

	// Any touch goes back to decks
	sceneAdd("any", contentRect)
	sceneAdd("any", actionRect)

	fbinkRefresh()
	drainTouch()
}

// ============================================================
// Main loop
// ============================================================

func rateAndAdvance(rating fsrs.Rating) Screen {
	card := core.FindCard(cards, currentCard.Front)
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
				fbinkClear()
				fbinkRefresh()
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
