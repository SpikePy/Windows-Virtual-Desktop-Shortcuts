package hotkeys

import "testing"

// ctrlAlt is the modifier state the shortcuts require.
var ctrlAlt = Modifiers{Ctrl: true, LeftAlt: true}

func TestFor(t *testing.T) {
	tests := []struct {
		name string
		vk   uint32
		mods Modifiers
		want Request
		ok   bool
	}{
		{
			name: "Ctrl+Alt+1 switches to the first desktop",
			vk:   VK1, mods: ctrlAlt,
			want: Request{Action: ActionSwitch, Index: 0}, ok: true,
		},
		{
			name: "Ctrl+Alt+9 switches to the ninth desktop",
			vk:   VK9, mods: ctrlAlt,
			want: Request{Action: ActionSwitch, Index: 8}, ok: true,
		},
		{
			name: "Ctrl+Alt+Shift+3 moves the window there",
			vk:   VK1 + 2, mods: Modifiers{Ctrl: true, LeftAlt: true, Shift: true},
			want: Request{Action: ActionMove, Index: 2}, ok: true,
		},
		{
			name: "Ctrl+Alt+Left switches to the previous desktop",
			vk:   VKLeft, mods: ctrlAlt,
			want: Request{Action: ActionSwitch, Index: -1, Relative: true}, ok: true,
		},
		{
			name: "Ctrl+Alt+Right switches to the next desktop",
			vk:   VKRight, mods: ctrlAlt,
			want: Request{Action: ActionSwitch, Index: 1, Relative: true}, ok: true,
		},
		{
			name: "Ctrl+Alt+Shift+Right moves the window to the next desktop",
			vk:   VKRight, mods: Modifiers{Ctrl: true, LeftAlt: true, Shift: true},
			want: Request{Action: ActionMove, Index: 1, Relative: true}, ok: true,
		},
		{
			name: "AltGr+7 is left alone so it can still type its character",
			vk:   VK1 + 6, mods: Modifiers{Ctrl: true, LeftAlt: false, RightAlt: true},
		},
		{
			name: "AltGr with left Alt also held is still ignored",
			vk:   VK1, mods: Modifiers{Ctrl: true, LeftAlt: true, RightAlt: true},
		},
		{
			name: "Win+Ctrl+Alt+1 is left to Windows",
			vk:   VK1, mods: Modifiers{Ctrl: true, LeftAlt: true, Win: true},
		},
		{name: "Alt+1 without Ctrl is not a shortcut", vk: VK1, mods: Modifiers{LeftAlt: true}},
		{name: "Ctrl+1 without Alt is not a shortcut", vk: VK1, mods: Modifiers{Ctrl: true}},
		{name: "no modifiers at all", vk: VK1},
		{name: "Ctrl+Alt+0 is not a shortcut", vk: 0x30, mods: ctrlAlt},
		{name: "Ctrl+Alt+Up is not a shortcut", vk: 0x26, mods: ctrlAlt},
		{name: "Ctrl+Alt+A is not a shortcut", vk: 0x41, mods: ctrlAlt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := For(tt.vk, tt.mods)
			if ok != tt.ok {
				t.Fatalf("For(0x%X, %+v) ok = %v, want %v", tt.vk, tt.mods, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("For(0x%X, %+v) = %+v, want %+v", tt.vk, tt.mods, got, tt.want)
			}
		})
	}
}

func TestModifiersHeld(t *testing.T) {
	tests := []struct {
		name string
		mods Modifiers
		want bool
	}{
		{"Ctrl and left Alt", ctrlAlt, true},
		{"Ctrl, left Alt and Shift", Modifiers{Ctrl: true, LeftAlt: true, Shift: true}, true},
		{"AltGr", Modifiers{Ctrl: true, RightAlt: true}, false},
		{"both Alt keys", Modifiers{Ctrl: true, LeftAlt: true, RightAlt: true}, false},
		{"with Win", Modifiers{Ctrl: true, LeftAlt: true, Win: true}, false},
		{"nothing", Modifiers{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mods.Held(); got != tt.want {
				t.Errorf("%+v.Held() = %v, want %v", tt.mods, got, tt.want)
			}
		})
	}
}

func TestWheel(t *testing.T) {
	prev := Request{Action: ActionSwitch, Index: -1, Relative: true}
	next := Request{Action: ActionSwitch, Index: 1, Relative: true}
	shift := Modifiers{Ctrl: true, LeftAlt: true, Shift: true}

	type step struct {
		delta int
		mods  Modifiers
		want  Request
		fire  bool
		ok    bool
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"Ctrl+Alt+scroll up switches to the previous desktop", []step{
			{delta: WheelNotch, mods: ctrlAlt, want: prev, fire: true, ok: true},
		}},
		{"Ctrl+Alt+scroll down switches to the next desktop", []step{
			{delta: -WheelNotch, mods: ctrlAlt, want: next, fire: true, ok: true},
		}},
		{"Shift moves the window instead", []step{
			{delta: -WheelNotch, mods: shift, want: Request{Action: ActionMove, Index: 1, Relative: true}, fire: true, ok: true},
		}},
		{"a fast spin still moves one desktop", []step{
			{delta: 3 * WheelNotch, mods: ctrlAlt, want: prev, fire: true, ok: true},
			{delta: 30, mods: ctrlAlt, ok: true},
		}},
		{"small steps add up to one notch", []step{
			{delta: -40, mods: ctrlAlt, ok: true},
			{delta: -40, mods: ctrlAlt, ok: true},
			{delta: -40, mods: ctrlAlt, want: next, fire: true, ok: true},
		}},
		{"changing direction starts over", []step{
			{delta: 80, mods: ctrlAlt, ok: true},
			{delta: -80, mods: ctrlAlt, ok: true},
			{delta: -40, mods: ctrlAlt, want: next, fire: true, ok: true},
		}},
		{"releasing the modifiers drops pending movement", []step{
			{delta: 80, mods: ctrlAlt, ok: true},
			{delta: 80},
			{delta: 80, mods: ctrlAlt, ok: true},
		}},
		{"plain scrolling is left alone", []step{
			{delta: WheelNotch},
		}},
		{"Ctrl+scroll (zoom) is left alone", []step{
			{delta: WheelNotch, mods: Modifiers{Ctrl: true}},
		}},
		{"AltGr+scroll is left alone", []step{
			{delta: WheelNotch, mods: Modifiers{Ctrl: true, RightAlt: true}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w Wheel
			for i, s := range tt.steps {
				req, fire, ok := w.Scroll(s.delta, s.mods)
				if req != s.want || fire != s.fire || ok != s.ok {
					t.Errorf("step %d: Scroll(%d) = %+v, fire %t, ok %t; want %+v, fire %t, ok %t",
						i, s.delta, req, fire, ok, s.want, s.fire, s.ok)
				}
			}
		})
	}
}
