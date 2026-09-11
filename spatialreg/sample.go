package spatialreg

import (
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/render"
)

const domainTolerance = 1e-9

// Sample trilinearly interpolates the displacement at a registered patient
// coordinate. It does not extrapolate beyond the deformation grid.
func (g *Grid) Sample(point render.Vec3) (render.Vec3, error) {
	if g == nil || !finiteVec(point) {
		return render.Vec3{}, fmt.Errorf("%w: missing grid or non-finite point", ErrInvalidObject)
	}
	if g.Dimensions[0] == 0 || g.Dimensions[1] == 0 || g.Dimensions[2] == 0 ||
		!finitePositive(g.Spacing.X) || !finitePositive(g.Spacing.Y) || !finitePositive(g.Spacing.Z) ||
		!validBasis(g.RowDirection, g.ColumnDirection, g.NormalDirection) {
		return render.Vec3{}, fmt.Errorf("%w: invalid deformation grid geometry", ErrInvalidObject)
	}
	delta := point.Sub(g.Origin)
	coordinates := [3]float64{
		delta.Dot(g.RowDirection) / g.Spacing.X,
		delta.Dot(g.ColumnDirection) / g.Spacing.Y,
		delta.Dot(g.NormalDirection) / g.Spacing.Z,
	}

	lower := [3]int{}
	upper := [3]int{}
	fraction := [3]float64{}
	for axis, coordinate := range coordinates {
		maximum := float64(g.Dimensions[axis] - 1)
		if !finite(coordinate) || coordinate < -domainTolerance || coordinate > maximum+domainTolerance {
			return render.Vec3{}, fmt.Errorf("%w: grid coordinate [%g %g %g]", ErrOutsideDomain, coordinates[0], coordinates[1], coordinates[2])
		}
		coordinate = math.Max(0, math.Min(maximum, coordinate))
		lower[axis] = int(math.Floor(coordinate))
		upper[axis] = min(lower[axis]+1, int(g.Dimensions[axis])-1)
		fraction[axis] = coordinate - float64(lower[axis])
	}

	var result render.Vec3
	for zChoice := 0; zChoice < 2; zChoice++ {
		z, wz := interpolationChoice(lower[2], upper[2], fraction[2], zChoice)
		for yChoice := 0; yChoice < 2; yChoice++ {
			y, wy := interpolationChoice(lower[1], upper[1], fraction[1], yChoice)
			for xChoice := 0; xChoice < 2; xChoice++ {
				x, wx := interpolationChoice(lower[0], upper[0], fraction[0], xChoice)
				weight := wx * wy * wz
				if weight == 0 {
					continue
				}
				value, ok := g.VectorAt(x, y, z)
				if !ok {
					return render.Vec3{}, fmt.Errorf("%w: contributing vector (%d,%d,%d)", ErrUndefinedDomain, x, y, z)
				}
				result = result.Add(value.Scale(weight))
			}
		}
	}
	if !finiteVec(result) {
		return render.Vec3{}, fmt.Errorf("%w: interpolation produced a non-finite vector", ErrUndefinedDomain)
	}
	return result, nil
}

func interpolationChoice(lower, upper int, fraction float64, choice int) (index int, weight float64) {
	if choice == 0 {
		return lower, 1 - fraction
	}
	return upper, fraction
}

func undefinedVector(value vector3f) bool {
	return math.IsNaN(float64(value.X)) && math.IsNaN(float64(value.Y)) && math.IsNaN(float64(value.Z))
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteVec(value render.Vec3) bool {
	return finite(value.X) && finite(value.Y) && finite(value.Z)
}

func validBasis(row, column, normal render.Vec3) bool {
	if !finiteVec(row) || !finiteVec(column) || !finiteVec(normal) {
		return false
	}
	return math.Abs(row.Length()-1) <= 1e-4 &&
		math.Abs(column.Length()-1) <= 1e-4 &&
		math.Abs(normal.Length()-1) <= 1e-4 &&
		math.Abs(row.Dot(column)) <= 1e-4 &&
		math.Abs(row.Dot(normal)) <= 1e-4 &&
		math.Abs(column.Dot(normal)) <= 1e-4
}
