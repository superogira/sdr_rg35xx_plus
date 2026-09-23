<?php
// Accept POST body and save as device.log (remote log upload from the SDR app)
$data = file_get_contents("php://input");
if ($data !== false && strlen($data) > 0) {
    file_put_contents(__DIR__ . "/device.log", $data);
    header("Content-Type: text/plain");
    echo "OK " . strlen($data) . " bytes";
} else {
    http_response_code(400);
    echo "ERROR: no data";
}
?>
