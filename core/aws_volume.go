package core

import (
	"fmt"
	"log"

	"github.com/supergiant/supergiant/types"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
)

type AwsVolume struct {
	core      *Core
	Blueprint *types.VolumeBlueprint
	Instance  *InstanceResource

	awsVol *ec2.Volume // used internally to store record of AWS vol
}

func (m *AwsVolume) name() string {
	return fmt.Sprintf("%s-%s", m.Instance.BaseName, *m.Blueprint.Name)
}

func (m *AwsVolume) id() *string {
	vol := m.awsVolume()
	if vol == nil {
		panic(fmt.Errorf("Trying to access ID of nil volume %#v", m))
	}
	return vol.VolumeId
}

// simple memoization of aws vol record
func (m *AwsVolume) awsVolume() *ec2.Volume {
	if m.awsVol == nil {
		m.loadAwsVolume()
	}
	return m.awsVol
}

func (m *AwsVolume) loadAwsVolume() {
	input := &ec2.DescribeVolumesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("tag:Name"),
				Values: []*string{
					aws.String(m.name()),
				},
			},
			// The following 3 state filters are used in order to not load volumes
			// that are in a deleting state.
			{
				Name: aws.String("status"),
				Values: []*string{
					aws.String("creating"),
					aws.String("available"),
					aws.String("in-use"),
				},
			},
		},
	}
	resp, err := m.core.EC2.DescribeVolumes(input)
	if err != nil {
		panic(err) // TODO this isn't a 404, so we need to figure out what could happen; probably implement retry
	}

	if len(resp.Volumes) > 0 {
		m.awsVol = resp.Volumes[0]
	}
	// Volume does not exist otherwise and that's fine
}

func (m *AwsVolume) createAwsVolume(snapshotID *string) error {
	volInput := &ec2.CreateVolumeInput{
		AvailabilityZone: aws.String(AwsAZ),
		VolumeType:       aws.String(m.Blueprint.Type),
		Size:             aws.Int64(int64(m.Blueprint.Size)),
		SnapshotId:       snapshotID,
	}

	awsVol, err := m.core.EC2.CreateVolume(volInput)
	if err != nil {
		return err
	}
	tagsInput := &ec2.CreateTagsInput{
		Resources: []*string{
			aws.String(*awsVol.VolumeId),
		},
		Tags: []*ec2.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(m.name()),
			},
		},
	}
	if _, err = m.core.EC2.CreateTags(tagsInput); err != nil {
		return err // TODO an error here means we create a hanging volume, since it does not get named
	}
	m.awsVol = awsVol

	return m.WaitForAvailable()
}

func (m *AwsVolume) createSnapshot() (*ec2.Snapshot, error) {
	input := &ec2.CreateSnapshotInput{
		Description: aws.String(m.name() + "-" + *m.Instance.Release().Timestamp),
		VolumeId:    m.id(),
	}
	snapshot, err := m.core.EC2.CreateSnapshot(input)
	if err != nil {
		return nil, err
	}
	waitInput := &ec2.DescribeSnapshotsInput{
		SnapshotIds: []*string{snapshot.SnapshotId},
	}
	if err := m.core.EC2.WaitUntilSnapshotCompleted(waitInput); err != nil {
		return snapshot, err // TODO
	}
	return snapshot, nil
}

func (m *AwsVolume) deleteSnapshot(snapshot *ec2.Snapshot) error {
	input := &ec2.DeleteSnapshotInput{
		SnapshotId: snapshot.SnapshotId,
	}
	_, err := m.core.EC2.DeleteSnapshot(input)
	return err
}

func (m *AwsVolume) Provision() error {
	if m.awsVolume() == nil {
		log.Printf("Creating EBS volume %s", m.name())
		return m.createAwsVolume(nil)
	}
	return nil
}

func (m *AwsVolume) WaitForAvailable() error {
	input := &ec2.DescribeVolumesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String("volume-id"),
				Values: []*string{
					m.id(),
				},
			},
		},
	}
	log.Printf("Waiting for EBS volume %s to be available", m.name())
	return m.core.EC2.WaitUntilVolumeAvailable(input)
}

// Delete deletes the EBS volume on AWS.
func (m *AwsVolume) Delete() error {
	if m.awsVolume() == nil { // || *m.awsVolume().State == "deleting" {
		return nil
	}
	if err := m.WaitForAvailable(); err != nil {
		return err
	}
	input := &ec2.DeleteVolumeInput{
		VolumeId: m.id(),
	}
	log.Printf("Deleting EBS volume %s", m.name())
	if _, err := m.core.EC2.DeleteVolume(input); err != nil {
		return err
	}
	m.awsVol = nil
	return nil
}

// NeedsResize returns true if the actual EBS size does not match the blueprint.
func (m *AwsVolume) NeedsResize() bool {
	if m.awsVolume() == nil {
		return false
	}
	return int64(m.Blueprint.Size) != *m.awsVolume().Size
}

// Resize snapshots the volume, creates a new volume from the snapshot, deletes
// the old volume, and renames the new volume to have the old name.
func (m *AwsVolume) Resize() error {
	log.Printf("Resizing EBS volume %s", m.name())
	snapshot, err := m.createSnapshot()
	if err != nil {
		return err
	}
	if err := m.Delete(); err != nil {
		return err
	}
	if err := m.createAwsVolume(snapshot.SnapshotId); err != nil {
		return err
	}
	if err := m.deleteSnapshot(snapshot); err != nil {
		log.Printf("Error deleting snapshot %s: %s", *snapshot.SnapshotId, err.Error())
	}
	return nil
}
