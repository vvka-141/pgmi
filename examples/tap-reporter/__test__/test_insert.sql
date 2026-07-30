DO $$
DECLARE
    v_item item;
BEGIN
    v_item := upsert_item('sprocket', 3);
    IF v_item.name IS DISTINCT FROM 'sprocket' THEN
        RAISE EXCEPTION 'upsert_item insert failed: expected sprocket, got %', v_item.name;
    END IF;
    IF v_item.qty IS DISTINCT FROM 3 THEN
        RAISE EXCEPTION 'upsert_item insert failed: expected qty 3, got %', v_item.qty;
    END IF;
END $$;
