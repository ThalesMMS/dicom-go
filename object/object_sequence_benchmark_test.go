package object

import (
	"fmt"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

var benchmarkSequenceItemsSink []*Object

func BenchmarkGetSequenceCold(b *testing.B) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	for _, itemCount := range []int{32, 256} {
		b.Run(fmt.Sprintf("items_%d", itemCount), func(b *testing.B) {
			obj := FromElements([]core.Element{
				benchmarkSequenceElement(sequenceTag, itemCount, 8),
			}, std.Dictionary)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				obj.sequenceCache = nil
				benchmarkSequenceItemsSink, _ = obj.GetSequence(sequenceTag)
			}
		})
	}
}

func BenchmarkGetSequenceRepeated(b *testing.B) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	for _, itemCount := range []int{32, 256} {
		b.Run(fmt.Sprintf("items_%d", itemCount), func(b *testing.B) {
			obj := FromElements([]core.Element{
				benchmarkSequenceElement(sequenceTag, itemCount, 8),
			}, std.Dictionary)
			// Prime the cache when the implementation provides one. The benchmark
			// measures the repeated-call path identified by issue #341.
			benchmarkSequenceItemsSink, _ = obj.GetSequence(sequenceTag)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				benchmarkSequenceItemsSink, _ = obj.GetSequence(sequenceTag)
			}
		})
	}
}

func benchmarkSequenceElement(tag core.Tag, itemCount, elementsPerItem int) core.Element {
	items := make([]core.DataSet, itemCount)
	for itemIndex := range items {
		elements := make([]core.Element, elementsPerItem)
		for elementIndex := range elements {
			elementTag := core.NewTag(0x0011, uint16(0x1000+elementIndex))
			elements[elementIndex] = core.NewRawElement(elementTag, core.VRLO, []byte("VALUE"))
		}
		items[itemIndex] = core.DataSet{Elements: elements}
	}
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
}
