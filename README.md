



# DB queries
Most common hosts  
`sqlite3 req.db "SELECT json_extract(request, '$.host'), count(json_extract(request, '$.host')) as count FROM requests GROUP by json_extract(request, '$.host') ORDER BY count;"`  


# TODO  
* Capture the requests in new tabs as well 