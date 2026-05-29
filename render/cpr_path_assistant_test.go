package render

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestFindCPRAssistedPathFollowsImageDerivedRoute(t *testing.T) {
	vol, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	start := vol.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4})
	end := vol.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4})

	got, err := FindCPRAssistedPath(context.Background(), vol, []Vec3{start, end}, CPRPathAssistantOptions{
		GridSpacingMM:   1,
		MarginMM:        3,
		OutputSpacingMM: 3,
		IntensityWeight: 80,
		MaxGridVoxels:   100_000,
	})
	if err != nil {
		t.Fatalf("FindCPRAssistedPath: %v", err)
	}
	if len(got) < 8 {
		t.Fatalf("path has %d points, want a resampled centerline", len(got))
	}
	if d := got[0].Sub(start).Length(); d > 1e-9 {
		t.Fatalf("start moved by %.6f mm", d)
	}
	if d := got[len(got)-1].Sub(end).Length(); d > 1e-9 {
		t.Fatalf("end moved by %.6f mm", d)
	}

	segments := [][2]Vec3{
		{{X: 3, Y: 10, Z: 4}, {X: 14, Y: 10, Z: 4}},
		{{X: 14, Y: 10, Z: 4}, {X: 14, Y: 22, Z: 4}},
		{{X: 14, Y: 22, Z: 4}, {X: 25, Y: 22, Z: 4}},
	}
	seenFirstBend := false
	seenSecondBend := false
	for i, patientPoint := range got {
		voxelPoint := vol.PatientToVoxel(patientPoint)
		minDistance := math.Inf(1)
		for _, segment := range segments {
			minDistance = math.Min(minDistance, distanceToSegment(voxelPoint, segment[0], segment[1]))
		}
		if minDistance > 2.25 {
			t.Fatalf("path point %d left the image-derived tube: voxel=%+v distance=%.2f", i, voxelPoint, minDistance)
		}
		seenFirstBend = seenFirstBend || voxelPoint.Sub(Vec3{X: 14, Y: 10, Z: 4}).Length() <= 2.5
		seenSecondBend = seenSecondBend || voxelPoint.Sub(Vec3{X: 14, Y: 22, Z: 4}).Length() <= 2.5
	}
	if !seenFirstBend || !seenSecondBend {
		t.Fatalf("path skipped a bend: first=%v second=%v", seenFirstBend, seenSecondBend)
	}
	for i := 1; i < len(got)-1; i++ {
		spacing := got[i].Sub(got[i-1]).Length()
		// Chord distance is slightly shorter than the requested arc-length
		// spacing when a sample straddles a sharp bend in the synthetic tube.
		if math.Abs(spacing-3) > 0.75 {
			t.Fatalf("resample spacing[%d]=%.3f mm, want about 3 mm", i, spacing)
		}
	}
}

func TestFindCPRAssistedPathConcatenatesWaypointSegments(t *testing.T) {
	vol, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	waypoints := []Vec3{
		vol.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4}),
		vol.VoxelToPatient(Vec3{X: 14, Y: 22, Z: 4}),
		vol.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4}),
	}

	got, err := FindCPRAssistedPath(context.Background(), vol, waypoints, CPRPathAssistantOptions{
		GridSpacingMM: 1, MarginMM: 3, OutputSpacingMM: 3, IntensityWeight: 80,
	})
	if err != nil {
		t.Fatalf("FindCPRAssistedPath: %v", err)
	}
	for _, waypoint := range waypoints {
		found := false
		for _, point := range got {
			if point.Sub(waypoint).Length() < 1e-9 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("waypoint %+v is not represented exactly in the concatenated path", waypoint)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].Sub(got[i-1]).Length() == 0 {
			t.Fatalf("duplicate point at concatenation index %d", i)
		}
	}
}

func TestFindCPRAssistedPathValidatesInputAndCancellation(t *testing.T) {
	vol, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	inside := vol.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4})
	out := vol.VoxelToPatient(Vec3{X: -2, Y: 10, Z: 4})

	for name, waypoints := range map[string][]Vec3{
		"one point":  {inside},
		"six points": {inside, inside, inside, inside, inside, inside},
		"outside":    {inside, out},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := FindCPRAssistedPath(context.Background(), vol, waypoints, CPRPathAssistantOptions{})
			if !errors.Is(err, ErrCPRPathAssistantInput) {
				t.Fatalf("error=%v, want ErrCPRPathAssistantInput", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = FindCPRAssistedPath(ctx, vol, []Vec3{inside, vol.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4})}, CPRPathAssistantOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error=%v, want context.Canceled", err)
	}
}

func TestFindCPRAssistedPathReportsBoundedSegmentMemoryFailure(t *testing.T) {
	vol, err := BuildVolume(bentTubeStack())
	if err != nil {
		t.Fatalf("BuildVolume: %v", err)
	}
	start := vol.VoxelToPatient(Vec3{X: 3, Y: 10, Z: 4})
	end := vol.VoxelToPatient(Vec3{X: 25, Y: 22, Z: 4})
	_, err = FindCPRAssistedPath(context.Background(), vol, []Vec3{start, end}, CPRPathAssistantOptions{
		GridSpacingMM: 1, MarginMM: 3, MaxGridVoxels: 1,
	})
	if !errors.Is(err, ErrCPRPathAssistantMemory) {
		t.Fatalf("error=%v, want ErrCPRPathAssistantMemory", err)
	}
	var segmentError *CPRPathAssistantSegmentError
	if !errors.As(err, &segmentError) || segmentError.From != 1 || segmentError.To != 2 {
		t.Fatalf("segment error=%#v, want waypoint pair 1-2", segmentError)
	}
}

func bentTubeStack() *Stack {
	const (
		cols  = 29
		rows  = 27
		depth = 9
	)
	stack := &Stack{PixelSpacing: []float64{1, 1}, SliceThickness: 1}
	for z := 0; z < depth; z++ {
		data := make([]byte, rows*cols)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				point := Vec3{X: float64(x), Y: float64(y), Z: float64(z)}
				segments := [][2]Vec3{
					{{X: 3, Y: 10, Z: 4}, {X: 14, Y: 10, Z: 4}},
					{{X: 14, Y: 10, Z: 4}, {X: 14, Y: 22, Z: 4}},
					{{X: 14, Y: 22, Z: 4}, {X: 25, Y: 22, Z: 4}},
				}
				for _, segment := range segments {
					if distanceToSegment(point, segment[0], segment[1]) <= 1.5 {
						data[y*cols+x] = 240
						break
					}
				}
			}
		}
		stack.Frames = append(stack.Frames, volumeTestFrame(rows, cols, data, z))
	}
	return stack
}

func distanceToSegment(point, start, end Vec3) float64 {
	span := end.Sub(start)
	denom := span.Dot(span)
	if denom == 0 {
		return point.Sub(start).Length()
	}
	t := point.Sub(start).Dot(span) / denom
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return point.Sub(start.Add(span.Scale(t))).Length()
}
