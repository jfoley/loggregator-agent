package v2_test

import (
	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	"code.cloudfoundry.org/loggregator-agent/pkg/egress/v2"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Tagger", func() {
	It("adds the given tags to all envelopes", func() {
		tags := map[string]string{
			"tag-one": "value-one",
			"tag-two": "value-two",
		}
		env := &loggregator_v2.Envelope{SourceId: "uuid"}

		tagger := v2.NewTagger(tags)
		Expect(tagger.Process(env)).ToNot(HaveOccurred())

		Expect(env.Tags["tag-one"]).To(Equal("value-one"))
		Expect(env.Tags["tag-two"]).To(Equal("value-two"))
	})

	It("does not write over tags if they already exist", func() {
		tags := map[string]string{
			"existing-tag": "some-new-value",
		}

		env := &loggregator_v2.Envelope{
			SourceId: "uuid",
			Tags: map[string]string{
				"existing-tag": "existing-value",
			},
		}

		tagger := v2.NewTagger(tags)
		Expect(tagger.Process(env)).ToNot(HaveOccurred())
		Expect(env.Tags["existing-tag"]).To(Equal("existing-value"))
	})

	It("does not write over deprecated tags if they already exist", func() {
		tags := map[string]string{
			"existing-tag": "some-new-value",
		}
		env := &loggregator_v2.Envelope{
			SourceId: "uuid",
			DeprecatedTags: map[string]*loggregator_v2.Value{
				"existing-tag": {
					Data: &loggregator_v2.Value_Text{
						Text: "existing-value",
					},
				},
			},
		}

		tagger := v2.NewTagger(tags)
		Expect(tagger.Process(env)).ToNot(HaveOccurred())
		Expect(env.Tags["existing-tag"]).To(Equal("existing-value"))
	})

	It("moves DesprecatedTags to Tags", func() {
		env := &loggregator_v2.Envelope{
			SourceId: "uuid",
			DeprecatedTags: map[string]*loggregator_v2.Value{
				"text-tag":    {Data: &loggregator_v2.Value_Text{Text: "text-value"}},
				"integer-tag": {Data: &loggregator_v2.Value_Integer{Integer: 502}},
				"decimal-tag": {Data: &loggregator_v2.Value_Decimal{Decimal: 0.23}},
			},
		}

		tagger := v2.NewTagger(map[string]string{})
		Expect(tagger.Process(env)).ToNot(HaveOccurred())

		Expect(env.Tags["text-tag"]).To(Equal("text-value"))
		Expect(env.Tags["integer-tag"]).To(Equal("502"))
		Expect(env.Tags["decimal-tag"]).To(Equal("0.23"))
	})
})
