# Test 1, Camunda: what the accepted deploy with a misspelled variable (amont) does

- A 6,000 purchase on that version, approved by the manager and finance: instance state ACTIVE, incident: True
- Incident at `finance_gw`: EXTRACT_VALUE_ERROR: Expected result of the expression 'financeApproved = true and amont > 5000' to be 'BOOLEAN', but was 'NULL'. The evaluation reported the following warnings: [NO_VARIABLE_FOUND] No variable found with name 'amont' [NOT_COMPARABLE] Can't compare 'null' with '5000' [INVALID_TYPE] Expected boolean but f
- Nobody is told except whoever watches Operate; the purchase waits until an operator fixes the model, migrates the instance and resolves the incident.
