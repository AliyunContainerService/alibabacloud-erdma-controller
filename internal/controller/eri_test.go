// Copyright 2023 Alibaba Cloud. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package controller_test

import (
	"context"
	"testing"

	"github.com/samber/lo"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/controller"
	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/alibabacloud-go/ecs-20140526/v4/client"
	"github.com/stretchr/testify/assert"
)

func TestEnsureEriTagsEmptyInputIsNoop(t *testing.T) {
	// EriClient.client is nil; should still return nil without panicking when
	// there is nothing to tag. Guards against accidentally calling the
	// OpenAPI for empty ENI lists (which the ECS server rejects).
	e := &controller.EriClient{}
	assert.NoError(t, e.EnsureEriTags(context.Background(), nil, "i-xxx"))
	assert.NoError(t, e.EnsureEriTags(context.Background(), []string{}, ""))
}

func TestSelectEriFromExist(t *testing.T) {
	tests := []struct {
		name                     string
		existENIs                []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
		queuePairCount           int
		cardCount                int
		expectedERIs             []*types.ERI
		expectedNeedCreate       []int
		expectedQueuePairPerCard int
		expectedError            bool
	}{
		{
			name:                     "No existing RDMA ENIs and Primary ENI",
			existENIs:                []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{},
			queuePairCount:           8,
			cardCount:                2,
			expectedERIs:             []*types.ERI{},
			expectedNeedCreate:       []int{0, 1},
			expectedQueuePairPerCard: 4,
			expectedError:            true,
		},
		{
			name: "Existing Primary ENIs is RDMA and with sufficient queue pairs",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      1,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-1",
					CardIndex:    0,
					QueuePair:    8,
					IsPrimaryENI: true,
					MAC:          "00:16:3e:00:00:00",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Existing Secondary RDMA ENI is RDMA and with sufficient queue pairs",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      1,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-2",
					CardIndex:    0,
					QueuePair:    8,
					IsPrimaryENI: false,
					MAC:          "00:16:3e:00:00:01",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Existing Secondary RDMA ENIs with sufficient queue pairs on cardIndex 1",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(1)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-2",
					CardIndex:    1,
					QueuePair:    8,
					IsPrimaryENI: false,
					MAC:          "00:16:3e:00:00:01",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Primary ENI is RDMA and used as card index 0, need create new on card index 1",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-primary"),
					Type:                        lo.ToPtr("Primary"),
					QueuePairNumber:             lo.ToPtr(int32(6)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-primary",
					IsPrimaryENI: true,
					CardIndex:    0,
					QueuePair:    6,
					MAC:          "00:16:3e:00:00:00",
				},
			},
			expectedNeedCreate:       []int{1},
			expectedQueuePairPerCard: 2,
			expectedError:            false,
		},
		{
			name: "Secondary ENI is RDMA and on both cardIndex 0 and 1, no need to create or convert new",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-primary"),
					Type:                        lo.ToPtr("Primary"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:03"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary1"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(6)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(1)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary2"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(2)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-secondary1",
					IsPrimaryENI: false,
					CardIndex:    1,
					QueuePair:    6,
					MAC:          "00:16:3e:00:00:00",
				},
				{
					ID:           "eni-secondary2",
					IsPrimaryENI: false,
					CardIndex:    0,
					QueuePair:    2,
					MAC:          "00:16:3e:00:00:01",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Secondary ENI is RDMA and both on cardIndex 0, no need to create or convert new",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-primary"),
					Type:                        lo.ToPtr("Primary"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:03"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary1"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(6)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary2"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(2)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-secondary1",
					IsPrimaryENI: false,
					CardIndex:    0,
					QueuePair:    6,
					MAC:          "00:16:3e:00:00:00",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Secondary ENI is RDMA and both on cardIndex 0 and cardIndex 1, but queue pair can not be divided",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-primary"),
					Type:                        lo.ToPtr("Primary"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:03"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary1"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(6)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(1)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-secondary2"),
					Type:                        lo.ToPtr("Secondary"),
					QueuePairNumber:             lo.ToPtr(int32(2)),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(1)),
					},
				},
			},
			queuePairCount: 9,
			cardCount:      3,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-secondary1",
					IsPrimaryENI: false,
					CardIndex:    1,
					QueuePair:    6,
					MAC:          "00:16:3e:00:00:00",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Need create on cardIndex 1",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(2)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:        "eni-1",
					CardIndex: 0,
					MAC:       "00:16:3e:00:00:00",
					QueuePair: 2,
				},
			},
			expectedNeedCreate:       []int{1},
			expectedQueuePairPerCard: 6,
			expectedError:            false,
		},
		{
			name: "Need convert on cardIndex 0",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(2)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(1)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      2,
			expectedERIs: []*types.ERI{
				{
					ID:        "eni-1",
					CardIndex: 1,
					MAC:       "00:16:3e:00:00:00",
					QueuePair: 2,
				},
				{
					ID:           "eni-2",
					CardIndex:    0,
					IsPrimaryENI: true,
					MAC:          "00:16:3e:00:00:01",
					QueuePair:    6,
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 6,
			expectedError:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &controller.EriClient{
				ManagedNonOwned: true,
			}
			eris, needCreate, queuePairPerCard, err := e.SelectEriFromExist(tt.existENIs, tt.queuePairCount, tt.cardCount)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				if needCreate == nil {
					needCreate = []int{}
				}
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedERIs, eris)
				assert.Equal(t, tt.expectedNeedCreate, needCreate)
				assert.Equal(t, tt.expectedQueuePairPerCard, queuePairPerCard)
			}
		})
	}
}

func TestSelectEriAllocatesQueuePairsForNewDevices(t *testing.T) {
	for _, tt := range []struct {
		name        string
		trafficMode string
		existingQP  int32
	}{
		{name: "reuse untagged primary ERI", trafficMode: "HighPerformance", existingQP: 60},
		{name: "convert standard primary ENI", trafficMode: "Standard"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &controller.EriClient{}
			eris, needCreate, queuePairs, err := e.SelectEriFromExist(
				[]*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{{
					NetworkInterfaceId:          lo.ToPtr("eni-primary"),
					Type:                        lo.ToPtr("Primary"),
					NetworkInterfaceTrafficMode: lo.ToPtr(tt.trafficMode),
					QueuePairNumber:             lo.ToPtr(tt.existingQP),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
				}}, 120, 2,
			)
			assert.NoError(t, err)
			assert.Equal(t, []*types.ERI{{
				ID: "eni-primary", IsPrimaryENI: true, QueuePair: 60, MAC: "00:16:3e:00:00:01",
			}}, eris)
			assert.Equal(t, []int{1}, needCreate)
			assert.Equal(t, 60, queuePairs)
		})
	}
}

func TestSelectEriNoManagedExists(t *testing.T) {
	tests := []struct {
		name                     string
		existENIs                []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet
		queuePairCount           int
		cardCount                int
		expectedERIs             []*types.ERI
		expectedNeedCreate       []int
		expectedQueuePairPerCard int
		expectedError            bool
	}{
		{
			name:                     "No existing RDMA ENIs and Primary ENI",
			existENIs:                []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{},
			queuePairCount:           8,
			cardCount:                2,
			expectedERIs:             []*types.ERI{},
			expectedNeedCreate:       []int{0, 1},
			expectedQueuePairPerCard: 4,
			expectedError:            true,
		},
		{
			name: "Reuse existing primary ERI even when all queue pairs are allocated",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      1,
			expectedERIs: []*types.ERI{{
				ID:           "eni-1",
				IsPrimaryENI: true,
				QueuePair:    8,
				MAC:          "00:16:3e:00:00:00",
			}},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Existing Secondary RDMA ENI is RDMA and with sufficient queue pairs",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Tags: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetTags{
						Tag: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetTagsTag{
							{
								TagKey:   lo.ToPtr("creator"),
								TagValue: lo.ToPtr("alibabacloud-erdma-controller"),
							},
						},
					},
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      1,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-2",
					CardIndex:    0,
					QueuePair:    8,
					IsPrimaryENI: false,
					MAC:          "00:16:3e:00:00:01",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            false,
		},
		{
			name: "Existing Secondary RDMA ENI is RDMA and with sufficient queue pairs no managed",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(8)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount:           8,
			cardCount:                1,
			expectedERIs:             nil,
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 0,
			expectedError:            true,
		},
		{
			name: "Existing Secondary RDMA ENI is RDMA and with insufficient queue pairs no managed",
			existENIs: []*client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSet{
				{
					NetworkInterfaceId:          lo.ToPtr("eni-1"),
					NetworkInterfaceTrafficMode: lo.ToPtr("Normal"),
					QueuePairNumber:             lo.ToPtr(int32(0)),
					Type:                        lo.ToPtr("Primary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:00"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
				{
					NetworkInterfaceId:          lo.ToPtr("eni-2"),
					NetworkInterfaceTrafficMode: lo.ToPtr("HighPerformance"),
					QueuePairNumber:             lo.ToPtr(int32(4)),
					Type:                        lo.ToPtr("Secondary"),
					MacAddress:                  lo.ToPtr("00:16:3e:00:00:01"),
					Attachment: &client.DescribeNetworkInterfacesResponseBodyNetworkInterfaceSetsNetworkInterfaceSetAttachment{
						NetworkCardIndex: lo.ToPtr(int32(0)),
					},
				},
			},
			queuePairCount: 8,
			cardCount:      1,
			expectedERIs: []*types.ERI{
				{
					ID:           "eni-1",
					CardIndex:    0,
					QueuePair:    4,
					IsPrimaryENI: true,
					MAC:          "00:16:3e:00:00:00",
				},
			},
			expectedNeedCreate:       []int{},
			expectedQueuePairPerCard: 4,
			expectedError:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &controller.EriClient{
				ManagedNonOwned: false,
			}
			eris, needCreate, queuePairPerCard, err := e.SelectEriFromExist(tt.existENIs, tt.queuePairCount, tt.cardCount)
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				if needCreate == nil {
					needCreate = []int{}
				}
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedERIs, eris)
				assert.Equal(t, tt.expectedNeedCreate, needCreate)
				assert.Equal(t, tt.expectedQueuePairPerCard, queuePairPerCard)
			}
		})
	}
}
