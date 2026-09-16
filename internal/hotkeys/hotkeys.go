// Package hotkeys decides which desktop action a keystroke or mouse-wheel
// movement stands for. It is deliberately free of any OS dependency: the
// Windows hooks read the input and modifier state and hand it here, so the
// rules can be tested on any platform.
package hotkeys

// Virtual-key codes this package recognises.
const (
	VK1     = 0x31
	VK9     = 0x39
	VKLeft  = 0x25
	VKRight = 0x27
)

// Action is what a shortcut does with its target desktop.
type Action int

const (
	// ActionSwitch switches the view to the target desktop.
	ActionSwitch Action = iota
	// ActionMove moves the focused window to the target desktop.
	ActionMove
)

// Modifiers is the modifier state at the time a key was pressed. LeftAlt
// and RightAlt are tracked separately because on layouts with AltGr (e.g.
// German) the right Alt key also reports Ctrl as held, and AltGr+digit
// types characters such as '{' that must keep working.
type Modifiers struct {
	Ctrl     bool
	LeftAlt  bool
	RightAlt bool
	Shift    bool
	Win      bool
}

// Held reports whether the modifiers are the ones this program's shortcuts
// use: Ctrl and the left Alt key, without the right Alt key or Win.
func (m Modifiers) Held() bool {
	return m.Ctrl && m.LeftAlt && !m.RightAlt && !m.Win
}

// Request is the desktop action a shortcut asks for.
type Request struct {
	Action Action
	// Index is the 0-based target desktop or, with Relative set, an offset
	// from the current desktop (-1 previous, +1 next).
	Index    int
	Relative bool
}

// For returns the request that vk stands for with the given modifiers, and
// whether it is a shortcut at all. 1-9 pick a desktop by number,
// Left/Right the previous/next one, and Shift turns switching into moving
// the focused window.
func For(vk uint32, m Modifiers) (Request, bool) {
	if !m.Held() {
		return Request{}, false
	}

	var req Request
	switch {
	case vk >= VK1 && vk <= VK9:
		req.Index = int(vk - VK1)
	case vk == VKLeft:
		req.Index, req.Relative = -1, true
	case vk == VKRight:
		req.Index, req.Relative = 1, true
	default:
		return Request{}, false
	}

	if m.Shift {
		req.Action = ActionMove
	}
	return req, true
}

// WheelNotch is how far one notch of a mouse wheel turns, in the units
// Windows reports. High-resolution wheels and touchpads report smaller
// steps that add up to it.
const WheelNotch = 120

// Wheel turns mouse-wheel movement into previous/next desktop requests:
// scrolling up (away from you) goes to the previous desktop, scrolling down
// to the next one, and Shift moves the focused window instead. The zero
// value is ready to use; it is not safe for concurrent use.
type Wheel struct {
	pending int
}

// Scroll takes one wheel event's delta (positive = up) with the modifiers
// held at the time. ok reports whether the event belongs to a shortcut and
// so should be kept from the application under the mouse; fire reports
// whether enough movement has built up for req to be carried out. Each
// request uses up all pending movement, so a fast spin switches one
// desktop at a time rather than racing through several.
func (w *Wheel) Scroll(delta int, m Modifiers) (req Request, fire, ok bool) {
	if !m.Held() {
		w.pending = 0
		return Request{}, false, false
	}
	if (delta > 0) != (w.pending > 0) {
		w.pending = 0 // changed direction: start over
	}
	w.pending += delta
	if w.pending > -WheelNotch && w.pending < WheelNotch {
		return Request{}, false, true
	}

	req = Request{Index: 1, Relative: true}
	if w.pending > 0 {
		req.Index = -1
	}
	if m.Shift {
		req.Action = ActionMove
	}
	w.pending = 0
	return req, true, true
}
