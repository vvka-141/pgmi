DO $$
DECLARE
    v_item item;
BEGIN
    v_item := upsert_item('widget', 99);
    IF v_item.qty IS DISTINCT FROM 99 THEN
        RAISE EXCEPTION 'upsert_item update failed: expected qty 99, got %', v_item.qty;
    END IF;

    SELECT * INTO v_item FROM item WHERE name = 'widget';
    IF v_item.qty IS DISTINCT FROM 99 THEN
        RAISE EXCEPTION 're-query after upsert: expected qty 99, got %', v_item.qty;
    END IF;
END $$;
