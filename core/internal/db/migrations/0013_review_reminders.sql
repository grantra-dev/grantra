-- Reviewers get one reminder before a campaign ends.
ALTER TABLE review_campaigns ADD COLUMN reminded_at timestamptz;
