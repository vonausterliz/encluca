package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icone dell'icona nella barra, disegnate nel codice (niente file da tenere allineati):
// cerchio del colore dello stato con un lucchetto bianco, aperto se la cassaforte è aperta.
var trayColors = map[string]color.RGBA{
	"closed":  {0x71, 0xBF, 0x44, 0xFF}, // verde del tema
	"open":    {0xF0, 0xCB, 0x00, 0xFF}, // giallo: file in chiaro
	"problem": {0xCE, 0x29, 0x29, 0xFF}, // rosso
}

const trayIconSize = 64

func trayIconImage(state string) *image.RGBA {
	n := trayIconSize
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	bg := trayColors[state]
	white := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	c := float64(n) / 2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if math.Hypot(fx-c, fy-c) <= c-1 {
				img.Set(x, y, bg)
			}
			// corpo del lucchetto
			if fx >= 20 && fx <= 44 && fy >= 30 && fy <= 48 {
				img.Set(x, y, white)
			}
			// arco: chiuso sopra il corpo, aperto spostato a destra e più in alto
			ax, ay := 32.0, 30.0
			if state == "open" {
				ax, ay = 38.0, 26.0
			}
			d := math.Hypot(fx-ax, fy-ay)
			if d >= 6.5 && d <= 10.5 && fy <= ay {
				img.Set(x, y, white)
			}
			if (math.Abs(fx-(ax-8.5)) <= 2 || math.Abs(fx-(ax+8.5)) <= 2) && fy >= ay && fy <= 31 && !(state == "open" && fx < ax) {
				img.Set(x, y, white)
			}
		}
	}
	return img
}

func trayIconPNG(state string) []byte {
	var b bytes.Buffer
	png.Encode(&b, trayIconImage(state))
	return b.Bytes()
}

// trayIconICO incapsula il PNG in un file ICO (formato accettato da Windows dal Vista in poi).
func trayIconICO(state string) []byte {
	p := trayIconPNG(state)
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, 1})                        // intestazione: icona, 1 immagine
	b.Write([]byte{trayIconSize, trayIconSize, 0, 0})                               // larghezza, altezza, colori, riservato
	binary.Write(&b, binary.LittleEndian, []uint16{1, 32})                          // piani, bit per pixel
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(p)), uint32(6 + 16)}) // dimensione, posizione
	b.Write(p)
	return b.Bytes()
}
