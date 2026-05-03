package main

import (
	"fmt"
	"image"
	"math"
	"os"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

func main() {
	application := app.New()
	window := application.NewWindow("dicom-go-viewer")
	window.Resize(fyne.NewSize(1000, 760))

	controller := &appController{window: window}
	switch len(os.Args) {
	case 1:
		controller.showChooser()
	case 2:
		if err := controller.openPath(os.Args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "load series: %v\n", err)
			dialog.ShowError(err, window)
			controller.showChooser()
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: go run . <series.zip|directory|file.dcm>")
		controller.showChooser()
	}

	window.ShowAndRun()
}

type appController struct {
	window fyne.Window
}

func (c *appController) showChooser() {
	c.window.Canvas().SetOnTypedKey(func(*fyne.KeyEvent) {})

	title := widget.NewLabelWithStyle("dicom-go-viewer", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	help := widget.NewLabel("Open a DICOM series from a .zip file, a folder, or a single .dcm file.")
	help.Alignment = fyne.TextAlignCenter
	help.Wrapping = fyne.TextWrapWord

	openFile := widget.NewButtonWithIcon("Open ZIP or DICOM file", theme.FolderOpenIcon(), func() {
		dialog.ShowFileOpen(func(reader fyne.URIReadCloser, err error) {
			if err != nil {
				dialog.ShowError(err, c.window)
				return
			}
			if reader == nil {
				return
			}
			uri := reader.URI()
			_ = reader.Close()
			c.openURI(uri)
		}, c.window)
	})
	openFolder := widget.NewButtonWithIcon("Open folder", theme.FolderOpenIcon(), func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil {
				dialog.ShowError(err, c.window)
				return
			}
			if uri == nil {
				return
			}
			c.openURI(uri)
		}, c.window)
	})

	c.window.SetContent(container.NewCenter(container.NewVBox(title, help, openFile, openFolder)))
}

func (c *appController) openURI(uri fyne.URI) {
	if uri == nil {
		return
	}
	if uri.Scheme() != "file" {
		dialog.ShowError(fmt.Errorf("viewer: only local files are supported, got %s URI", uri.Scheme()), c.window)
		return
	}
	if err := c.openPath(uri.Path()); err != nil {
		dialog.ShowError(err, c.window)
	}
}

func (c *appController) openPath(input string) error {
	series, err := loadSeries(input)
	if err != nil {
		return err
	}
	viewer := newViewer(series)
	c.window.Canvas().SetOnTypedKey(func(event *fyne.KeyEvent) {
		switch event.Name {
		case fyne.KeyLeft:
			viewer.previous()
		case fyne.KeyRight:
			viewer.next()
		}
	})
	c.window.SetContent(viewer.content())
	return nil
}

type viewer struct {
	series       *Series
	index        int
	window       Window
	setting      bool
	image        *canvas.Image
	status       *widget.Label
	sliceLabel   *widget.Label
	sliceSlider  *widget.Slider
	centerLabel  *widget.Label
	centerSlider *widget.Slider
	widthLabel   *widget.Label
	widthSlider  *widget.Slider
}

func newViewer(series *Series) *viewer {
	initial := series.Frames[0].DefaultWindow
	if err := validateWindow(initial); err != nil {
		initial = Window{Center: defaultWindowCenter, Width: defaultWindowWidth}
	}
	imageView := canvas.NewImageFromImage(blankImage(512, 512))
	imageView.FillMode = canvas.ImageFillContain
	imageView.SetMinSize(fyne.NewSize(512, 512))

	v := &viewer{
		series:      series,
		window:      initial,
		image:       imageView,
		status:      widget.NewLabel(""),
		sliceLabel:  widget.NewLabel(""),
		centerLabel: widget.NewLabel(""),
		widthLabel:  widget.NewLabel(""),
	}
	v.status.Wrapping = fyne.TextWrapWord

	maxSlice := math.Max(1, float64(len(series.Frames)))
	v.sliceSlider = widget.NewSlider(1, maxSlice)
	v.sliceSlider.Step = 1
	v.sliceSlider.OnChanged = func(value float64) {
		if v.setting {
			return
		}
		v.setIndex(int(math.Round(value)) - 1)
	}

	centerMin, centerMax, widthMax := sliderRanges(initial)
	v.centerSlider = widget.NewSlider(centerMin, centerMax)
	v.centerSlider.Step = 1
	v.centerSlider.OnChanged = func(value float64) {
		if v.setting {
			return
		}
		v.window.Center = value
		v.render()
	}
	v.widthSlider = widget.NewSlider(1, widthMax)
	v.widthSlider.Step = 1
	v.widthSlider.OnChanged = func(value float64) {
		if v.setting {
			return
		}
		v.window.Width = math.Max(1, value)
		v.render()
	}

	v.syncControls()
	v.render()
	return v
}

func (v *viewer) content() fyne.CanvasObject {
	previous := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), v.previous)
	next := widget.NewButtonWithIcon("", theme.NavigateNextIcon(), v.next)
	reset := widget.NewButtonWithIcon("Reset window", theme.ViewRefreshIcon(), v.resetWindow)

	sliceControls := container.NewBorder(nil, nil, previous, next, v.sliceSlider)
	centerControls := container.NewBorder(nil, nil, widget.NewLabel("WC"), v.centerLabel, v.centerSlider)
	widthControls := container.NewBorder(nil, nil, widget.NewLabel("WW"), v.widthLabel, v.widthSlider)
	controls := container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Slice"), v.sliceLabel, sliceControls),
		centerControls,
		widthControls,
		container.NewHBox(reset),
	)
	return container.NewBorder(v.status, controls, nil, nil, container.NewMax(v.image))
}

func (v *viewer) previous() {
	v.setIndex(v.index - 1)
}

func (v *viewer) next() {
	v.setIndex(v.index + 1)
}

func (v *viewer) resetWindow() {
	frame := v.series.Frames[v.index]
	window := frame.DefaultWindow
	if err := validateWindow(window); err != nil {
		window = Window{Center: defaultWindowCenter, Width: defaultWindowWidth}
	}
	v.window = window
	v.syncControls()
	v.render()
}

func (v *viewer) setIndex(index int) {
	if len(v.series.Frames) == 0 {
		return
	}
	v.index = int(clampFloat(float64(index), 0, float64(len(v.series.Frames)-1)))
	v.syncControls()
	v.render()
}

func (v *viewer) syncControls() {
	v.setting = true
	defer func() { v.setting = false }()

	frame := v.series.Frames[v.index]
	centerMin, centerMax, widthMax := sliderRanges(frame.DefaultWindow)
	v.centerSlider.Min = centerMin
	v.centerSlider.Max = centerMax
	v.widthSlider.Min = 1
	v.widthSlider.Max = widthMax
	v.window.Center = clampFloat(v.window.Center, centerMin, centerMax)
	v.window.Width = clampFloat(v.window.Width, 1, widthMax)
	v.sliceSlider.SetValue(float64(v.index + 1))
	v.centerSlider.SetValue(v.window.Center)
	v.widthSlider.SetValue(v.window.Width)
	v.sliceLabel.SetText(fmt.Sprintf("%d / %d", v.index+1, len(v.series.Frames)))
	v.centerLabel.SetText(fmt.Sprintf("%.0f", v.window.Center))
	v.widthLabel.SetText(fmt.Sprintf("%.0f", v.window.Width))
	v.centerSlider.Refresh()
	v.widthSlider.Refresh()
}

func (v *viewer) render() {
	if len(v.series.Frames) == 0 {
		v.image.Image = blankImage(512, 512)
		v.status.SetText("No frames loaded")
		v.image.Refresh()
		return
	}
	v.centerLabel.SetText(fmt.Sprintf("%.0f", v.window.Center))
	v.widthLabel.SetText(fmt.Sprintf("%.0f", v.window.Width))

	frame := v.series.Frames[v.index]
	img, err := renderFrame(frame, v.window)
	if img == nil {
		img = blankImage(512, 512)
	}
	v.image.Image = img
	v.image.Refresh()
	v.status.SetText(statusText(v.series, frame, v.index, img, err))
}

func statusText(series *Series, frame Frame, index int, img image.Image, renderErr error) string {
	size := img.Bounds().Size()
	parts := []string{
		fmt.Sprintf("Slice %d/%d", index+1, len(series.Frames)),
		fmt.Sprintf("%dx%d", size.X, size.Y),
		strings.TrimSpace(frame.TransferSyntaxName),
		frame.SourceName,
	}
	if frame.TransferSyntaxUID != "" {
		parts[2] = fmt.Sprintf("%s (%s)", parts[2], frame.TransferSyntaxUID)
	}
	if renderErr != nil {
		parts = append(parts, "error: "+renderErr.Error())
	}
	if len(series.LoadErrors) > 0 {
		parts = append(parts, fmt.Sprintf("load warnings: %d", len(series.LoadErrors)))
	}
	return strings.Join(parts, "  |  ")
}

func sliderRanges(window Window) (centerMin, centerMax, widthMax float64) {
	centerSpan := math.Max(2048, math.Abs(window.Center)*2+window.Width*2)
	centerMin = window.Center - centerSpan
	centerMax = window.Center + centerSpan
	widthMax = math.Max(4096, window.Width*4)
	return centerMin, centerMax, widthMax
}
