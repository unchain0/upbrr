// Copyright (c) 2025-2026, Audionut and the autobrr contributors.
// SPDX-License-Identifier: GPL-2.0-or-later

package mns

import (
	"testing"

	"github.com/autobrr/upbrr/internal/metadata"
	"github.com/autobrr/upbrr/internal/trackers"
	"github.com/autobrr/upbrr/internal/trackers/impl/unit3d"
	"github.com/autobrr/upbrr/pkg/api"
)

func TestMNSReleaseNamePolicyUsesNoGroupSuffix(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		tag  string
		want string
	}{
		{name: "blank", want: "Example Movie 2026 1080p WEB-DL-NOGRP"},
		{
			name: "equivalent no-group marker",
			tag:  "-NOGROUP",
			want: "Example Movie 2026 1080p WEB-DL-NOGRP",
		},
		{
			name: "existing suffix",
			tag:  "-NOGRP",
			want: "Example Movie 2026 1080p WEB-DL-NOGRP",
		},
		{
			name: "real group",
			tag:  "-GRP",
			want: "Example Movie 2026 1080p WEB-DL-GRP",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			generated := metadata.BuildReleaseName(api.ReleaseNameRequest{
				Category:   "MOVIE",
				Type:       "WEBDL",
				Title:      "Example Movie",
				Year:       2026,
				Resolution: "1080p",
				Source:     "Web",
				Tag:        test.tag,
			}, api.NopLogger{})
			if generated.GeneratedName == nil {
				t.Fatal("BuildReleaseName did not produce a structured document")
			}
			prepared, failure := trackers.PrepareInputWithReleaseNamePolicy(trackers.PreparationInput{
				Tracker: "MNS",
				Meta: api.UploadSubject{
					ReleaseName:      generated.Name,
					ReleaseNameNoTag: generated.NameNoTag,
					GeneratedName:    generated.GeneratedName,
					Tag:              test.tag,
				},
			}, unit3d.NewWithProfile(Profile()).ReleaseNamePolicy())
			if failure != nil {
				t.Fatal(failure)
			}
			got, err := prepared.ReviewedUploadName()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("reviewed name = %q, want %q", got, test.want)
			}
		})
	}

	policy := unit3d.NewWithProfile(Profile()).ReleaseNamePolicy()
	if policy.ID != "unit3d/mns/v1" || policy.Structured == nil || policy.Resolver != nil {
		t.Fatalf("MNS policy = %#v", policy)
	}
}
