-- Access reviews certify eligibilities too: who may activate just-in-time
-- access, whether or not an activation is running. The activations
-- themselves are not reviewed: they last hours, and revoking the eligibility
-- ends them.
ALTER TABLE review_items
    ADD COLUMN eligibility_id uuid REFERENCES access_eligibilities (id) ON DELETE SET NULL,
    ADD CONSTRAINT review_items_one_source CHECK (assignment_id IS NULL OR eligibility_id IS NULL);
