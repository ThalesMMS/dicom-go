package object

import (
	"fmt"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func BenchmarkObjectRemovePrivateTags(b *testing.B) {
	for _, size := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprintf("elements_%d", size), func(b *testing.B) {
			elements := privateElementsForBenchmark(size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				obj := FromElements(elements, nil)
				for _, elem := range elements {
					obj.Remove(elem.Tag())
				}
			}
		})
	}
}

func BenchmarkObjectUpdatePrivateTags(b *testing.B) {
	for _, size := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprintf("elements_%d", size), func(b *testing.B) {
			elements := privateElementsForBenchmark(size)
			updated := make([]core.Element, len(elements))
			for i, elem := range elements {
				updated[i] = core.NewRawElement(elem.Tag(), core.VRLO, []byte("UPDATED"))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				obj := FromElements(elements, nil)
				for _, elem := range updated {
					obj.Put(elem)
				}
			}
		})
	}
}

func privateElementsForBenchmark(count int) []core.Element {
	elements := make([]core.Element, count)
	for i := range elements {
		tag := core.NewTag(0x0011, uint16(0x1000+i))
		elements[i] = core.NewRawElement(tag, core.VRLO, []byte("PRIVATE"))
	}
	return elements
}
