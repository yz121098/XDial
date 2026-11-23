package trayicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

type State int

const (
	StateDisconnected State = iota
	StateConnecting
	StateConnected
	StateProxyOnly
)

type Badge int

const (
	BadgeNone Badge = iota
	BadgeUpdate
	BadgeError
)

type Spec struct {
	State       State
	Badge       Badge
	PulseFrame  int  // 0..11 for 75ms connecting animation
	DarkTaskbar bool // true on dark Windows 11 taskbar -> light ink
}

var holeCenters = [6][2]float64{
	{11.9, 5.75},
	{9.16, 3.65},
	{5.75, 4.10},
	{3.65, 6.84},
	{4.10, 10.25},
	{6.84, 12.35},
}

func clamp01(v float64) float64 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	return v
}

// coverage converts signed distance (negative = inside) in grid units to [0, 1] alpha
// using 1-pixel smooth anti-aliasing ramp at the target pixel scale.
func coverage(sd, aaRadius float64) float64 {
	return clamp01(0.5 - sd/(2*aaRadius))
}

func distPoint(px, py, cx, cy float64) float64 {
	dx := px - cx
	dy := py - cy
	return math.Hypot(dx, dy)
}

func distSegment(px, py, x1, y1, x2, y2 float64) float64 {
	vx := x2 - x1
	vy := y2 - y1
	wx := px - x1
	wy := py - y1
	lenSq := vx*vx + vy*vy
	t := 0.0
	if lenSq > 0 {
		t = clamp01((wx*vx + wy*vy) / lenSq)
	}
	projX := x1 + t*vx
	projY := y1 + t*vy
	return math.Hypot(px-projX, py-projY)
}

func holePulseAlpha(frame, hole int) float64 {
	f := float64(((frame % 12) + 12) % 12)
	phase := f/12.0 - float64(hole)/6.0
	phase = phase - math.Floor(phase)
	return 0.25 + 0.75*(1.0-math.Abs(2.0*phase-1.0))
}

// RenderPNG renders a crisp 64x64 RGBA PNG matching MenuBarStatusIcon.swift's 16x16 geometry.
func RenderPNG(spec Spec) []byte {
	const size = 64
	const scale = float64(size) / 16.0
	const aa = 1.0 / scale

	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	ink := color.NRGBA{R: 28, G: 35, B: 39, A: 255}
	danger := color.NRGBA{R: 136, G: 65, B: 57, A: 255}
	if spec.DarkTaskbar {
		ink = color.NRGBA{R: 245, G: 247, B: 248, A: 255}
		danger = color.NRGBA{R: 217, G: 140, B: 125, A: 255}
	}

	for py := 0; py < size; py++ {
		gy := (float64(py) + 0.5) / scale
		for px := 0; px < size; px++ {
			gx := (float64(px) + 0.5) / scale

			baseAlpha := 0.0
			if spec.Badge == BadgeError {
				// Error replaces the base dial with hollow ring + exclamation mark
				dCenter := distPoint(gx, gy, 8.0, 8.0)
				ringCov := coverage(math.Abs(dCenter-6.2)-0.75, aa)
				barCov := coverage(distSegment(gx, gy, 8.0, 4.6, 8.0, 9.2)-0.85, aa)
				dotCov := coverage(distPoint(gx, gy, 8.0, 11.3)-1.05, aa)
				baseAlpha = math.Max(ringCov, math.Max(barCov, dotCov))
			} else {
				switch spec.State {
				case StateDisconnected:
					dCenter := distPoint(gx, gy, 8.0, 8.0)
					ringCov := coverage(math.Abs(dCenter-6.2)-0.75, aa)
					holeCov := 0.0
					for i := 0; i < 6; i++ {
						hc := coverage(distPoint(gx, gy, holeCenters[i][0], holeCenters[i][1])-1.0, aa)
						if hc > holeCov {
							holeCov = hc
						}
					}
					// Single composited layer at 0.45 opacity
					baseAlpha = math.Max(ringCov, holeCov) * 0.45

				case StateConnecting:
					dCenter := distPoint(gx, gy, 8.0, 8.0)
					ringCov := coverage(math.Abs(dCenter-6.2)-0.75, aa)
					holeAlpha := 0.0
					for i := 0; i < 6; i++ {
						hc := coverage(distPoint(gx, gy, holeCenters[i][0], holeCenters[i][1])-1.0, aa)
						ha := hc * holePulseAlpha(spec.PulseFrame, i)
						if ha > holeAlpha {
							holeAlpha = ha
						}
					}
					baseAlpha = math.Max(ringCov, holeAlpha)

				case StateConnected, StateProxyOnly:
					dCenter := distPoint(gx, gy, 8.0, 8.0)
					discCov := coverage(dCenter-6.6, aa)
					// Destination-out cutouts for 6 holes and finger stop
					cutout := coverage(distSegment(gx, gy, 9.84, 9.84, 12.1, 12.1)-0.7, aa)
					for i := 0; i < 6; i++ {
						hc := coverage(distPoint(gx, gy, holeCenters[i][0], holeCenters[i][1])-1.15, aa)
						if hc > cutout {
							cutout = hc
						}
					}
					baseAlpha = discCov * (1.0 - cutout)

					if spec.State == StateProxyOnly {
						// Destination-out bite at (13.4, 13.4) r=3.1, then solid port dot r=1.9
						biteCov := coverage(distPoint(gx, gy, 13.4, 13.4)-3.1, aa)
						baseAlpha = baseAlpha * (1.0 - biteCov)
						portDot := coverage(distPoint(gx, gy, 13.4, 13.4)-1.9, aa)
						baseAlpha = math.Max(baseAlpha, portDot)
					}
				}
			}

			outR, outG, outB, outA := float64(ink.R), float64(ink.G), float64(ink.B), clamp01(baseAlpha)

			// Overlay update dot on top (Error has higher priority and suppresses update dot)
			if spec.Badge == BadgeUpdate {
				dotCov := coverage(distPoint(gx, gy, 12.9, 3.1)-2.6, aa)
				if dotCov > 0 {
					// Cut slightly around update dot for crisp contrast, then source-over blend
					haloCut := coverage(distPoint(gx, gy, 12.9, 3.1)-3.2, aa)
					bgA := outA * (1.0 - haloCut*0.65)
					finalA := dotCov + bgA*(1.0-dotCov)
					if finalA > 0 {
						outR = (float64(danger.R)*dotCov + outR*bgA*(1.0-dotCov)) / finalA
						outG = (float64(danger.G)*dotCov + outG*bgA*(1.0-dotCov)) / finalA
						outB = (float64(danger.B)*dotCov + outB*bgA*(1.0-dotCov)) / finalA
						outA = finalA
					}
				}
			}

			if outA > 0 {
				img.SetNRGBA(px, py, color.NRGBA{
					R: uint8(math.Round(outR)),
					G: uint8(math.Round(outG)),
					B: uint8(math.Round(outB)),
					A: uint8(math.Round(outA * 255.0)),
				})
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// RenderICO wraps RenderPNG in a standard Windows ICO container (PNG-compressed 64x64 frame)
// because Windows Shell_NotifyIcon requires .ico format for system tray icons.
func RenderICO(spec Spec) []byte {
	pngBytes := RenderPNG(spec)
	var out bytes.Buffer
	// ICONDIR header (6 bytes): Reserved=0, Type=1 (ICO), Count=1
	_ = binary.Write(&out, binary.LittleEndian, uint16(0))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	// ICONDIRENTRY (16 bytes)
	out.WriteByte(64)                                             // Width
	out.WriteByte(64)                                             // Height
	out.WriteByte(0)                                              // ColorCount
	out.WriteByte(0)                                              // Reserved
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))        // Planes
	_ = binary.Write(&out, binary.LittleEndian, uint16(32))       // BitCount
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(pngBytes)))
	_ = binary.Write(&out, binary.LittleEndian, uint32(6+16))     // ImageOffset
	out.Write(pngBytes)
	return out.Bytes()
}
